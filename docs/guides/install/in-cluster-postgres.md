# How to run Postgres in the cluster

By default Postgres is outside the cluster: the installation supplies its URL in the `exemplar-database` Secret. For evaluation or a small installation without a managed database, add `components/postgres` to an overlay of `base` or `direct`. The quickstart overlays already do this.

!!! warning "Not a production database"
    One replica, no replication, no backups, no point-in-time recovery, no TLS between the pods and Postgres, and the app holds superuser. Use a managed Postgres 16 with pgvector for production, or an operator such as CloudNativePG; [Move to an outside database](#move-to-an-outside-database) is how to switch.

## 1. Add the component

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - <path>/deploy/k8s/base
components:
  - <path>/deploy/k8s/components/postgres
```

It adds `exemplar-postgres`: a one-replica StatefulSet of `pgvector/pgvector:0.8.0-pg16` (the image the store tests use, pinned by digest), a Service on 5432, a 50Gi volume on the default storage class, and a NetworkPolicy that admits only the webhook, API, gateway, worker, runner and learning pods, and `exemplar-pgbouncer` when [`components/pgbouncer`](pool-runner-connections.md) is there too.

## 2. Create the Secrets

The component declares both, empty. On a first install exemplar-api fills them as it starts in setup mode ([Quickstart, step 5](quickstart.md#5-register-the-github-app)): it sees the component's Service and no database URL yet, and keeps a password that is already there. To set them yourself instead, before applying:

- `exemplar-postgres` with `password`, the superuser's password;
- `exemplar-database` with `url` set to `postgres://exemplar:<password>@exemplar-postgres.exemplar.svc:5432/exemplar` (URL-encode the password).

The binaries run migrations at start, as with an outside database. The app connects as the superuser, which `CREATE EXTENSION vector` needs.

## 3. Set the volume

Set the volume's size and class in your overlay, as for search. A drain or reschedule is downtime: there is no PodDisruptionBudget, since with one replica it would only block node drains.

## How it runs

- As the image's `postgres` user (UID 999) under the namespace's `restricted` level, with a read-only root and emptyDirs for the socket directory, `/tmp` and a 256 MiB `/dev/shm`. `PGDATA` is a subdirectory of the volume, since initdb refuses a mount root holding `lost+found`.
- Probes run `pg_isready` over TCP, so the pod isn't ready while first-start init runs on the socket only. A startup probe allows 10 minutes of crash recovery before liveness counts.
- SIGTERM alone is Postgres's smart shutdown, which waits for every pooled client to leave. A preStop `pg_ctl stop --mode=fast --timeout=30` stops it first, under a 45 s grace.

## Move to an outside database

To move a quickstart to a managed Postgres (for example [RDS or Aurora](rds-aurora.md)):

1. Create the outside database, role and extension ([How to install for production, step 2](production.md#2-create-the-database)). To keep what the in-cluster database holds, copy it over first with `pg_dump` and `pg_restore`; otherwise the new database starts empty.
2. In the overlay, remove `components/postgres` and the patch on the `exemplar-postgres` StatefulSet's volume.
3. Replace `url` in `exemplar-database` with the outside database's URL. The Secret is still there with the in-cluster URL setup wrote, and setup fills only empty keys, so it never changes it for you. [How to set up RDS or Aurora PostgreSQL, step 4](rds-aurora.md#4-put-the-url-in-the-secret) has a way to write it that keeps the password out of the shell history.
4. Apply the overlay, then delete what the component left. `kubectl apply` doesn't prune, so these keep running until deleted, and the volume holds the old data until its claim goes:

    ```bash
    kubectl -n exemplar delete statefulset/exemplar-postgres service/exemplar-postgres networkpolicy/exemplar-postgres serviceaccount/exemplar-postgres secret/exemplar-postgres
    ```

    ```bash
    kubectl -n exemplar delete pvc/data-exemplar-postgres-0
    ```

5. Restart the services, which read the URL as they start. exemplar-api, if it started before there was a database, is still in setup mode until then:

    ```bash
    kubectl -n exemplar rollout restart deployment
    ```
