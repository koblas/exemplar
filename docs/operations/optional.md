# Optional pieces

**Grafana.** Provision [`dashboards/`](https://github.com/koblas/exemplar/tree/main/dashboards): boards for rule health, adjudication, operations, lanes, the auto-approve audit, counter-metrics, shift-left and telemetry, plus alert rules. Point the `exemplar-pg` datasource at a read-only role on the read replica. The grants are listed in [Dashboards](dashboards.md).

**Telemetry.** Create an `exemplar-otel` ConfigMap with `OTEL_EXPORTER_OTLP_ENDPOINT`, and an optional Secret with `OTEL_EXPORTER_OTLP_HEADERS`. Every container picks them up. Only OTLP over HTTP is supported. The `allow-otel` NetworkPolicy opens port 4318 to an in-cluster OpenTelemetry Collector.

**Config CI.** The workflows in `exemplar-config` (`.github/workflows/eval.yml`):
- validate every config PR;
- run the adversarial set;
- evaluate changed rules through `/v1/evaluate` (`exemplar eval --remote`).

The eval job needs repository variables `EXEMPLAR_API_URL`, `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE`. It also needs secrets for an Auth0 client whose only permission is `evaluate:<org>/exemplar-config`. On the exemplar-api side, set `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE` in `exemplar-settings`.

**Rule exports.** On the setup page (port-forward to exemplar-api during setup), also create the exports app, install it only on the repos that opt in (**never on exemplar-config**), and apply `exemplar-exports-app.yaml`. Then add `exports.targets` to each repo's file in `exemplar-config`. To add it after setup, register it by hand with Contents and Pull requests **Read & write** and no events, and create `exemplar-exports-app` with `app-id` and `private-key`.

**Auto-approve** stays off until the Phase 5 exit criteria are met (spec §16). Turn it on per repo in `exemplar-config` (`auto_approve` in the repo file, and `auto_approve: true` in `global.yaml`).
- Repos whose rulesets require code-owner review use `approval_mode: merge_policy_check`.
- They require the `exemplar/merge-policy` check instead of counting Exemplar's approval, and keep a break-glass bypass actor.

**The compile CronJob** is suspended. It runs model-written rules only inside a user-namespace sandbox, which the `restricted` Pod Security level normally refuses. Enable it only once the cluster provides unprivileged user namespaces; see the [deployment reference](../reference/deployment.md).
