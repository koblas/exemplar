# Reference

Exact, complete descriptions of every configurable thing. These pages describe; the [Guides](../guides/index.md) say what to do. `go test ./docs` fails when the code has an environment variable, a CLI subcommand or a schema property these pages don't list.

| Page | Covers |
|---|---|
| [exemplar-config](config/index.md) | The repository layout, CODEOWNERS and workflows, and one page per file schema. |
| [Environment variables](environment-variables.md) | Every `EXEMPLAR_*` variable, per binary, with where it is set. |
| [Secrets and ConfigMaps](secrets-and-configmaps.md) | Every Secret key and setting an installation supplies. |
| [Workloads, ports and probes](workloads.md) | The manifests: builds, inbound, each workload's ports, probes, replicas and shutdown. |
| [The CLI](cli.md) | Every `exemplar` subcommand and flag. |
| [Web UI pages](web-ui.md) | Every page under `/ui/`, the sign-in routes, feedback links and `/v1/evaluate`. |
| [Feedback commands](feedback-commands.md) | Slash commands, links, reactions and signals, who may use them, and what each records. |
| [Dashboards and alerts](dashboards.md) | Every Grafana board, the alert rules, and the database grants. |
| [Releases and images](releases.md) | Image tags, release assets, pinning. |
