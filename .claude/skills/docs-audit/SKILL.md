---
name: docs-audit
description: "Read-only audit of the user documentation site against the Divio quadrants and the code: quadrant mixing, uncovered env vars, CLI commands, schema fields, Secrets, UI pages and commands, internal references, stale notes. Reports findings; changes nothing."
argument-hint: "[area: tutorials | concepts | guides | reference | a page path | all]"
disable-model-invocation: true
---

# Audit the documentation

Audit this scope: $ARGUMENTS (default: all). Do not edit, format, commit or
build anything. Produce a report.

The site is `mkdocs.yml` plus `docs/` without `docs/spec`, `docs/plans` and
`docs/security`. The quadrant rules are in `.claude/skills/docs/SKILL.md` and
`docs/guides/develop/docs.md`; read them first.

## 1. Quadrant mixing

For each page in scope, read it and classify every section:

- A **tutorial** with explanation beyond what a step needs, or with options.
- A **how-to** that explains why (background paragraphs, principles), that
  repeats a field table from reference, or whose title does not start with
  "How to".
- A **concept** page with numbered steps, commands to run, or field tables.
- A **reference** page that instructs ("you should", "set X to", imperative
  steps) or that explains rationale at length.
- Any page that addresses more than one persona.

Report each as `path:line: <quadrant> page contains <what>; move to <page>`.

## 2. Coverage against the code

Compare, without trusting the test alone:

| Source | Against |
|---|---|
| `"EXEMPLAR_*"` names in `cmd/`, `internal/`, `learning/src` (non-test files) | `docs/reference/environment-variables.md` |
| `Use:` fields in `cmd/exemplar/*.go`, and each command's `Flags()` | `docs/reference/cli.md` (every subcommand heading, every flag row) |
| top-level and nested `properties` in `schema/*.schema.json` | `docs/reference/config/<name>.md` |
| Secret and ConfigMap names and keys in `deploy/k8s/**` | `docs/reference/secrets-and-configmaps.md` |
| routes in `internal/web/web.go` and `internal/api/*.go` | `docs/reference/web-ui.md` |
| `commandKinds` in `internal/worker/commands.go`, `Reasons` in `internal/feedbacklink`, the signal and reaction kinds | `docs/reference/feedback-commands.md` |
| workloads, ports, probes, grace periods and CronJob schedules in `deploy/k8s/core/*.yaml` | `docs/reference/workloads.md` |
| boards and alerts in `dashboards/` | `docs/reference/dashboards.md` |
| `exemplar-learn` subcommands (`cmd/exemplar-learn/main.go`) and the Python CLI | `docs/reference/workloads.md`, `docs/concepts/rule-lifecycle.md` |

Report each gap as `<thing> in <source> has no entry on <page>`, and each
documented thing that no longer exists in the code as stale.

## 3. Internal references

Grep the public pages for `§`, `FR-`, `PRD`, `T-0.`, `Phase <digit>`, and for
links into `docs/spec`, `docs/plans` or `docs/security` that are not GitHub
URLs. Report each with its line.

## 4. Stale notes

Find "not built yet", "not yet", "waits on", "suspended", "pending", "for
now", "TODO" and dated notes ("as of", "2026-") on public pages, and check
each against the code: is the thing now built, the CronJob still suspended,
the date still the latest? Report what is stale with the evidence.

## 5. Navigation and links

Every page in `docs/` (outside the excluded directories) is in `mkdocs.yml`
`nav`; every entry in the `redirects` map points at an existing page; the
landing page's capability map and the `guides/index.md` and
`reference/index.md` lists name every page in their area.

## Report

Group findings by section above, most important first within each: coverage
gaps and stale facts, then quadrant mixing, then internal references, then
navigation. Give `path:line` for each, and what to do. End with a short count
per section and the commands to re-check (`go test ./docs`, `make docs`).
