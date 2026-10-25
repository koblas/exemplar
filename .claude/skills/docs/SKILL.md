---
name: docs
description: "Use when a behavior change needs documenting, when asked to write or update a page of the user documentation site, or when a PR touches cmd/, internal/, schema/ or deploy/ without touching docs/. Puts the change in the right Divio quadrant (tutorial, how-to, concept, reference), keeps one audience per page, then runs make docs."
argument-hint: "<behavior change, or the page to write or fix>"
---

# Document a change

Document this: $ARGUMENTS

The site is `mkdocs.yml` plus `docs/` (not `docs/spec`, `docs/plans`,
`docs/security`: design documents the site excludes). It follows the Divio
documentation system. Read `docs/guides/develop/docs.md` for the reader-facing
version of these rules, and `.claude/CLAUDE.md` for the repository map.

## 1. Find what changed

For a behavior change, read the diff (`git diff origin/main...HEAD`, or the
files named) and list what a reader can now observe: a new or changed
`EXEMPLAR_*` variable, CLI flag or subcommand, schema property, Secret or
ConfigMap key, web UI page, feedback command, workload, probe, port, CronJob,
check-run text, or default. For a page request, read the page and the code it
describes. Never document from memory: every statement traces to code,
manifests, schemas or the example config.

## 2. Decide the quadrant for each item

| The reader wants to… | Quadrant | Directory | Page style |
|---|---|---|---|
| Learn by doing, starting from nothing | Tutorial | `docs/tutorials/` | Numbered steps that always work on a fresh install; one path; the minimum explanation each step needs; ends with "Next". |
| Get one task done | How-to | `docs/guides/<persona>/` | Title starts with "How to"; assumes the reader knows what Exemplar is; numbered steps; options as Material content tabs (`=== "Cloudflare Tunnel"` / `=== "Ingress"`); links to reference for every field, never repeats the field table. |
| Understand how or why | Concept | `docs/concepts/` | Prose and Mermaid diagrams; no steps, no commands, no field tables; may link the spec file on GitHub for the long form. |
| Look something up | Reference | `docs/reference/` | Tables, complete, in the code's own words; describes, never instructs ("X does Y", not "set X to Y"); one row per variable, flag, field, page, Secret key. |

Rules that hold on every page:

- **How-to never explains; reference never instructs; concepts never list fields.** A sentence in the wrong quadrant moves to the right page and leaves a link.
- **Positioning flows through.** The site's lead is the review bottleneck: AI-assisted coding multiplied PR volume, review did not scale, standards slip under the load. Exemplar's two jobs are to hold the review standard (owned rules, verified findings) and to absorb the volume (a verdict on every PR: approved with no human needed, or routed to humans with reasons; rules shipped to agents before the PR). A landing, concept or tutorial page that introduces a feature says which job it serves. Say "approved with no human review needed" and "routed to humans" for the outcomes; "auto-approve" is the mechanism's name in config, guides and reference. Facts about the industry go on Why Exemplar with a footnote to the source; other pages link there rather than repeating numbers.
- **One audience per page.** Personas: PR authors, human reviewers, rule owners, platform teams (install, operate), security and compliance, coding agents, contributors (develop). A page for installers doesn't address rule owners.
- **No internal references.** Never `spec §x`, `FR-xx`, `PRD`, `T-0.x`, `Phase N` (a test fails). Say what the code does; link `docs/spec/technical-spec.md` on GitHub only where the reader needs the design rationale.
- **Keep every fact.** When splitting or moving text, nothing is dropped; when a fact is stale, fix it against the code.
- **Links are relative** between pages (`../reference/cli.md`); links into the repository are GitHub URLs on `main`.
- **A moved page** gets a line in the `redirects` plugin map in `mkdocs.yml`, and every reference to its old path in the repository (manifest comments, READMEs, workflows) is updated.

## 3. Write

Most behavior changes touch two pages: the how-to that covers the task and the
reference page that lists the thing. A new feature also gets a paragraph on the
concept page that explains the area, and a row in the landing page's
capability map if it is a new area. A new persona-facing surface (a command,
a UI page, a slash command) gets a how-to for the persona that uses it.

Reference pages that `go test ./docs` checks:

| Code | Page | Entry |
|---|---|---|
| `"EXEMPLAR_*"` in `cmd/`, `internal/`, `learning/src` | `docs/reference/environment-variables.md` | the name in backticks, in the table of the binary that reads it |
| `Use: "<name>"` in `cmd/exemplar/*.go` | `docs/reference/cli.md` | a heading `### \`exemplar <name>\`` with a flag table |
| top-level `properties` of `schema/<name>.schema.json` | `docs/reference/config/<name>.md` | the property in backticks in the Fields table |

Also keep `docs/reference/secrets-and-configmaps.md` (Secret keys, ConfigMap
keys), `docs/reference/workloads.md` (ports, probes, replicas, shutdown,
CronJob schedules), `docs/reference/web-ui.md` (every `/ui/` page and route),
`docs/reference/feedback-commands.md` (every command, link, reaction, signal)
and `docs/reference/dashboards.md` (every board and alert) complete by hand.

## 4. Persona checklist

Before finishing, ask for each persona whether the change reaches them, and
if so whether their guide says what they do now:

- **PR authors:** what they see on the PR, what they reply, what the CLI does.
- **Human reviewers:** what counts as their feedback.
- **Rule owners:** rule fields, eval cases, config CI, the playground, candidates, adjudication, the digest.
- **Platform teams:** install, upgrade, settings, Secrets, workloads, the runbook, kill switches.
- **Security and compliance:** eligibility, retention, what leaves the cluster, SARIF, auto-approve gates.
- **Coding agents:** exported instruction files, `exemplar review`, service tokens.
- **Contributors:** devenv, checks, this site.

## 5. Check

```bash
make docs
```

runs `go test ./docs` and the strict build. Fix every error. Then
`golangci-lint run` if Go test code changed. Report what pages changed and which
quadrant each item went to.
