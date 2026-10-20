# How to run Postgres in the cluster

By default Postgres is outside the cluster: the installation supplies its URL in the `exemplar-database` Secret. For evaluation or a small installation without a managed database, add `components/postgres` to an overlay of `base` or `direct`. The quickstart overlays already do this.

!!! warning "Not a production database"
    One replica, no replication, no backups, no point-in-time recovery, no TLS between the pods and Postgres, and the app holds superuser. Use a managed Postgres 16 with pgvector for production, or an operator such as CloudNativePG.

## 1. Add the component

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - <path>/deploy/k8s/base
components:
  - <path>/deploy/k8s/components/postgres
```

It adds `exemplar-postgres`: a one-replica StatefulSet of `pgvector/pgvector:0.8.0-pg16` (the image the store tests use, pinned by digest), a Service on 5432, a 50Gi volume on the default storage class, and a NetworkPolicy that admits only the webhook, API, gateway, worker and learning pods.

## 2. Create the Secrets

- `exemplar-postgres` with `password`, the superuser's password;
- `exemplar-database` with `url` set to `postgres://exemplar:<password>@exemplar-postgres.exemplar.svc:5432/exemplar` (URL-encode the password).

The binaries run migrations at start, as with an outside database. The app connects as the superuser, which `CREATE EXTENSION vector` needs.

## 3. Set the volume

Set the volume's size and class in your overlay, as for search. A drain or reschedule is downtime: there is no PodDisruptionBudget, since with one replica it would only block node drains.

## How it runs

- As the image's `postgres` user (UID 999) under the namespace's `restricted` level, with a read-only root and emptyDirs for the socket directory, `/tmp` and a 256 MiB `/dev/shm`. `PGDATA` is a subdirectory of the volume, since initdb refuses a mount root holding `lost+found`.
- Probes run `pg_isready` over TCP, so the pod isn't ready while first-start init runs on the socket only. A startup probe allows 10 minutes of crash recovery before liveness counts.
- SIGTERM alone is Postgres's smart shutdown, which waits for every pooled client to leave. A preStop `pg_ctl stop --mode=fast --timeout=30` stops it first, under a 45 s grace.
