# How to pool runner connections

Each review runs in a Kubernetes Job of its own, and each runner opens up to 4 Postgres connections. With hundreds of reviews in flight, that exhausts the database's `max_connections`. Put a pooler in transaction mode between the runners and Postgres; everything else keeps its direct connection.

!!! warning "Runners only"
    The worker (control), webhook, API and learning jobs take session-level advisory locks (the worker's review slots per repository, migrations at startup, the learning jobs' run locks), which a transaction-mode pooler breaks. Every binary but the runner stays on the direct URL; only the runner template reads the pooled one, and `go test ./deploy` fails if anything else references it.

=== "PgBouncer in the cluster"

    1. Add the component to your overlay, beside `components/postgres` or with a managed database:

        ```yaml
        components:
          - <path>/deploy/k8s/components/pgbouncer
        ```

    2. Create the `exemplar-pgbouncer` Secret with two keys:

        - `databases.ini`, where Postgres is:

            ```ini
            [databases]
            exemplar = host=db.internal port=5432 dbname=exemplar
            ```

            With `components/postgres`, the host is `exemplar-postgres.exemplar.svc`.

        - `userlist.txt`, the role the runners connect as, with its password in plain text or as its SCRAM secret:

            ```text
            "exemplar" "<password>"
            ```

    3. Add `runner-url` to the `exemplar-database` Secret, pointing at the pooler:

        ```text
        postgres://exemplar:<password>@exemplar-pgbouncer.exemplar.svc:5432/exemplar
        ```

        Keep `url` as it is: the direct URL.

    4. Size `default_pool_size` in the `exemplar-pgbouncer` ConfigMap (patch it in your overlay) so that replicas × pool size stays under the database's `max_connections`, leaving room for the direct clients.

=== "A cloud pooler"

    1. Turn on the provider's pooler in transaction mode. One that pins a client to a server connection for the session does not reduce connections.
    2. Add `runner-url` to the `exemplar-database` Secret with the pooler's URL.
    3. If the pooler does not support prepared statements in transaction mode, append `default_query_exec_mode=exec` to that URL.

## When it takes effect

Nothing restarts: each new runner Job reads `runner-url` when it starts. Runners already running finish on the direct URL. Without `runner-url`, runners connect directly, as before.

The settings are on [Environment variables](../../reference/environment-variables.md#exemplar-worker) and [Secrets and ConfigMaps](../../reference/secrets-and-configmaps.md); the pooler's ports, probes and shutdown are on [Workloads, ports and probes](../../reference/workloads.md#workloads).
