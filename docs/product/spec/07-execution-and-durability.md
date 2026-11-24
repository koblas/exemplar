## 7. Execution and durability

Reviews are minutes long and cheap to redo, learning jobs are table-driven batches, and human waits are GitHub PRs. Based on that, Exemplar uses **River** (Postgres-backed job queue) and idempotent steps; no workflow engine is deployed.

| Mechanism | Implementation |
|---|---|
| Idempotent units | `reviews` unique on `(repo, pr, head_sha, config_version, caller)` ; `rule_runs` unique on `(review_id, rule_id, rule_version)` . A retried job skips completed rule runs and verified findings. |
| Transactional enqueue | Business writes and the next job's insert share one `pgx.Tx` (River `InsertTx` ). |
| Side-effect idempotency | Before posting, the poster looks for an existing review on `head_sha` carrying the review's hidden marker. Export PRs are idempotent on `content_hash` ; teach proposals on `(rule_id, teach_class, window)` . |
| Batch pipelines | Every mined comment carries `extract_status` ; Message Batches ids are recorded; drivers resume `pending` and `in_batch` rows. |
| Cancellation | Superseded reviews are cancelled with River's `JobCancel` , which propagates to the running job's `context.Context` on any pod. With the Job launcher the dispatcher holds the job's context and deletes the Job on cancel; the runner also watches its claim row and exits when it is marked cancelled. |
| Runners | A runner that dies leaves a River job that times out or errors and is retried by a new runner, which skips completed rule runs. A claim is single-use: a retry gets a new claim with new tokens, and the old row expires. |
| Scheduled jobs | `concurrencyPolicy: Forbid` on Kubernetes **and** a Postgres advisory lock in every job entrypoint, Go and Python (§2.4 C4), so a manual run cannot overlap a scheduled one on any platform. **As built:** a session-level `pg_try_advisory_lock` keyed by FNV-64a of `exemplar/job/<name>` on a dedicated connection (`store.TryJobLock`, `exemplar_learning.db.job_lock`), taken by `exemplar-learn`'s dispatcher for every subcommand, dry runs included, and by Python `cluster` and `rank`; a run that finds it held logs and exits 0. |
| Search index | New generations are built beside the serving one and swapped atomically; a crash leaves the previous generation serving. |
| Step structure | Each step is `func(ctx, Input) (Output, error)` with side effects behind injected interfaces. |

---
