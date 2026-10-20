# How to set up Grafana

The boards and alerts in [`dashboards/`](https://github.com/koblas/exemplar/tree/main/dashboards) are provisioned files, reviewed like code. What each board shows is on [Dashboards and alerts](../../reference/dashboards.md).

## 1. Create the read-only role

On the Postgres read replica, grant a Grafana role `SELECT` on the views `finding_outcomes`, `rule_weekly_stats` and `review_daily_stats`, and on the tables `findings`, `rule_runs`, `reviews`, `rules`, `feedback_events`, `approval_decisions`, `model_calls`, `river_job`, `exports`, `export_runs`, `pr_merges` and `pr_links`, and nothing else. The boards show aggregates, rule ids, repo names and links only; they never select finding text, code, fixes or reasons, and `go test ./dashboards` refuses a query that does.

## 2. Provision the datasources

Copy `dashboards/provisioning/datasource.yaml`. It defines `exemplar-pg`, pointed at the replica with the role above, and `exemplar-metrics`, a Prometheus-compatible endpoint for the OTel metrics the services export (`EXEMPLAR_GRAFANA_METRICS_URL`). The `telemetry.json` board needs the second; see [How to send telemetry](telemetry.md).

## 3. Mount the boards and alerts

Mount the `*.json` boards at `/var/lib/grafana/dashboards/exemplar` with `dashboards/provisioning/dashboards.yaml`, and `provisioning/alerts.yaml` at `/etc/grafana/provisioning/alerting`.

The alert rules carry `team=platform`; contact points and notification policies belong to your Grafana installation.

## What is not built yet

- The program board, whose inputs (LGTM recall, time to merge) aren't recorded yet.
- The counter-metrics on owned paths no team member viewed and on human substantive-comment volume, whose inputs aren't recorded.
