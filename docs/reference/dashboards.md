# Dashboards and alerts

Grafana boards and alert rules in [`dashboards/`](https://github.com/koblas/exemplar/tree/main/dashboards), provisioned from files and reviewed like code; [How to set up Grafana](../guides/operate/grafana.md) wires them up. Each board reads the Postgres read replica through the `exemplar-pg` datasource (`provisioning/datasource.yaml`); `telemetry.json` reads OTel metrics through `exemplar-metrics`, a Prometheus-compatible endpoint (`EXEMPLAR_GRAFANA_METRICS_URL`) behind whatever the services' `OTEL_EXPORTER_OTLP_*` variables export to. Metric definitions live in the migration-controlled views (`finding_outcomes`, `rule_weekly_stats`, `review_daily_stats`), so the boards, the digest and auto-demotion agree. `go test ./dashboards` runs every board's SQL.

## Boards

| Board | What |
|---|---|
| `rule-health.json` | Per rule: posted, accepted, rejected, ignored, human agreed, acceptance, weighted eFP, and the Wilson lower bound demotion uses; weekly acceptance and eFP trends. |
| `adjudication.json` | The owner queue (author wrong/disagree without an adjudication) per team, with its oldest age; uphold and overturn by rule. |
| `operations.json` | Reviews, incomplete and degraded runs, coverage; cost and duration (informational, no targets); rule runs that did not complete; River queue depth by queue and by repo; model calls by stage and provider. |
| `auto-approve.json` | Decisions per day, and every decision with Stage 1 reasons, merge safety and Stage 2, linked to the PR; auto-approved merges later reverted or incident-linked; each repo's kill switch as its latest decision saw it. |
| `telemetry.json` | From OTel metrics (the `exemplar-metrics` datasource): enqueue-to-first-comment p50/p95, review duration, model calls by outcome with the 429/5xx share, model latency by stage, GitHub rate limit remaining and responses by status. |
| `counter-metrics.json` | Change failure (reverted or incident-linked) of auto-approved against human-approved merges, revert rate and merges by week, incidents on quiet quick merges, and every failed merge. |
| `lanes.json` | Findings by lane and state, held back by reason, verifier refutation by rule, fixes (with fix, valid, applied), acceptance by lane. |
| `baseline.json` | Exemplar against the baseline reviewer on the same PR heads. |
| `shift-left.json` | Rule exports: repos exporting, current, more than two config versions behind, hand-edited files; drift per repo with its export PR; files by target and state; upstream effectiveness per exported rule. |

## Alerts

`provisioning/alerts.yaml`, mounted at `/etc/grafana/provisioning/alerting`. Rules carry `team=platform`; contact points and notification policies belong to the Grafana installation.

| Alert | Fires when |
|---|---|
| Auto-approved merge reverted or incident-linked | within 28 days of the merge |
| Auto-approved merges failing more often than human-approved ones | once there are 20 auto-approved merges |
| Approval from a degraded or incomplete review | in the last day |

## Access

The boards show aggregates, rule ids, repo names and links only. They never show finding text, code, fixes or reasons; `go test ./dashboards` refuses a query that selects them. A PR link lands on GitHub, which enforces repo permissions.

## The database role

Grant the Grafana role `SELECT` on the views and on `findings`, `rule_runs`, `reviews`, `rules`, `feedback_events`, `approval_decisions`, `model_calls`, `river_job`, `exports`, `export_runs`, `pr_merges` and `pr_links`, and nothing else. Mount the boards at `/var/lib/grafana/dashboards/exemplar` (`provisioning/dashboards.yaml`).

## Not built yet

- The program board, whose inputs (LGTM recall, time to merge) aren't recorded yet.
- The counter-metrics on owned paths no team member viewed and on human substantive-comment volume, whose inputs aren't recorded.
