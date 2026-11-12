# How to set up RDS or Aurora PostgreSQL

Create Exemplar's database on Amazon RDS for PostgreSQL or Aurora PostgreSQL, from inside the cluster, when the database has no public address. The SQL is the one in [How to install for production, step 2](production.md#2-create-the-database); this page is how to run it there.

!!! note "What the database needs"
    PostgreSQL **16 or later** with the `vector` extension available (RDS and Aurora PostgreSQL 16 have it). The database's security group must allow TCP 5432 from the cluster's nodes, or its pods' subnets.

## 1. Get the admin password

With an RDS-managed admin password, it is in Secrets Manager. Find the secret:

```bash
aws rds describe-db-clusters --db-cluster-identifier <cluster> --query 'DBClusters[0].MasterUserSecret.SecretArn' --output text
```

For an RDS instance instead of an Aurora cluster, `aws rds describe-db-instances --db-instance-identifier <instance> --query 'DBInstances[0].MasterUserSecret.SecretArn'`. Then read it:

```bash
aws secretsmanager get-secret-value --secret-id '<secret-arn-or-name>' --query SecretString --output text
```

Quote the id in single quotes. The secret's name starts with `rds!`, and in double quotes or unquoted, bash and zsh history expansion rewrites the `!`: Secrets Manager then answers `ResourceNotFoundException` for a secret RDS lists.

## 2. Open a psql session

Run a throwaway `postgres:16` pod in a namespace without a default-deny NetworkPolicy, such as `default`. The `exemplar` namespace denies traffic to and from any pod its policies don't name:

```bash
kubectl -n default run psql-tmp --rm -it --restart=Never --image=postgres:16 -- psql "host=<endpoint> dbname=postgres user=<admin-user> sslmode=require"
```

psql asks for the password, then shows the `postgres=>` prompt. If the pod hangs before asking, the database's security group isn't admitting the cluster.

The RDS console's query editor works too when the cluster has the Data API enabled, but it can't run `\c`: connect it to the `exemplar` database for the last statement.

## 3. Create the role, the database and the extension

As the admin user:

```sql
CREATE ROLE exemplar LOGIN PASSWORD '<password>';
GRANT exemplar TO CURRENT_USER;
CREATE DATABASE exemplar OWNER exemplar;
\c exemplar
CREATE EXTENSION IF NOT EXISTS vector;
```

psql answers `CREATE ROLE`, `GRANT ROLE`, `CREATE DATABASE`, `You are now connected to database "exemplar"` and `CREATE EXTENSION`, in that order.

The `GRANT` is what lets the admin user, which isn't a superuser on RDS or Aurora, create a database owned by `exemplar` on PostgreSQL 16. The extension is created here because the `exemplar` role can't; the first migration then finds it.

## 4. Put the URL in the Secret

Build the URL with the password URL-encoded and `sslmode=require`, without the password reaching the shell history or the process list:

```bash
read -rs PGPASS
```

```bash
URL="postgres://exemplar:$(printf %s "$PGPASS" | jq -sRr @uri)@<endpoint>:5432/exemplar?sslmode=require"
```

```bash
printf %s "$URL" | kubectl -n exemplar create secret generic exemplar-database --from-file=url=/dev/stdin --dry-run=client -o yaml | kubectl apply -f -
```

```bash
unset PGPASS URL
```

The third command prints `secret/exemplar-database created`, or `configured` when the Secret was already there.

`kubectl apply` replaces `url` in a Secret that is already there, which an in-cluster database's setup may have written ([How to run Postgres in the cluster](in-cluster-postgres.md#move-to-an-outside-database)).

## 5. Restart and check

The services read the URL as they start, and apply the migrations one pod at a time:

```bash
kubectl -n exemplar rollout restart deployment
```

```bash
kubectl -n exemplar logs deploy/exemplar-api --tail=20
```

The log has no database error.

## If a statement fails

- **`must be able to SET ROLE "exemplar"`** on `CREATE DATABASE`: the `GRANT exemplar TO CURRENT_USER` didn't run first.
- **`permission denied to create extension "vector"`**: the extension was created as `exemplar`, or not created before the first start. Run `CREATE EXTENSION IF NOT EXISTS vector;` in the `exemplar` database as the admin user, and restart.
- **`extension "vector" is not available`**: the engine version has no pgvector. Upgrade to PostgreSQL 16 or later.

## With a coding agent

Steps 1 to 4 handle the admin password and the `exemplar` role's password, so they are the person's: each is a **HANDOFF** in the sense of [How to run the quickstart from a coding agent](quickstart-for-agents.md#rules-for-the-agent). The person runs them in their own terminal, and the agent never reads the Secrets Manager secret, the psql session or `exemplar-database`.

An agent can do the parts with no secret in them: find the secret's ARN and the endpoint (step 1's first command, and `aws rds describe-db-clusters --query 'DBClusters[0].Endpoint'`), fill them into the commands for the person to run, and run step 5. To confirm step 4 without printing it:

```bash
kubectl -n exemplar get secret exemplar-database -o jsonpath='{.data.url}' | wc -c
```

A count above 0 means `url` is set.
