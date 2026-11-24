# Plans

The map of Exemplar's plans: which document says what, how to read the references in code and commits, and where to find what is built and what is not. Status is not kept here: open work is in [GitHub Issues](https://github.com/koblas/exemplar/issues), each labelled with its phase, and on the shared [Idea to delivery](https://github.com/users/koblas/projects/2) project. A PRD requirement that is not built ends its id cell with `(not built: #N)`.

## Documents

| Document | What it is |
|---|---|
| [`docs/product/spec/`](../product/README.md) | **The technical specification** (Draft 1.4), one file per top-level section, and the **canonical copy**: change the spec here. It covers architecture, pipeline, data model, security and the delivery plan, and is the source for every `§` reference. |
| [`docs/product/prd/`](../product/README.md) | **The PRD** (v0.11), one file per top-level section, and the **canonical copy**: change the PRD here. It covers the problem, goals, personas, and the functional requirements (`FR-…`) the spec implements, with sample interactions (§17). |
| [`docs/plans/ready-to-install.md`](ready-to-install.md) | The plan for making a first installation short and checkable (setup in the cluster, the GitHub App manifest flow, one root key, a setup page, release manifests). Frozen; its work is done or in issues. |
| [`docs/plans/demo.md`](demo.md) | The design for the **end-to-end demo** on a sandbox org: the four beats the audience sees, what produces each, the prerequisites from outside the repository, the demo kit, and the code the demo still needs. Status is in epic #181. |
| [`docs/specs/`](../specs/README.md) | Feature specs for user-visible changes, with case tables, and their retros. |
| [`docs/security/phase-0-data-handling.md`](../security/phase-0-data-handling.md) | What the code does with repository data, for Security's review and sign-off. |
| [`docs/reference/workloads.md`](../reference/deployment.md) | How to deploy: images, manifests, what an installation supplies, and the rollout risks. |
| [`.claude/CLAUDE.md`](../../.claude/CLAUDE.md) | Repository conventions: binaries, commands, checks, safety rules, work tracking. |

The Phase 0 plan with its dated head-start log, the ready-to-install §7 log and the backlog were removed on 2026-11-24; read them at [`3a23e68`](https://github.com/koblas/exemplar/tree/3a23e68d545cfa5e1b8140c732ab5a79e83d8891/docs/plans). Backlog ideas are issues labelled `idea`.

## Reading the references

- **`§6.15`** is a section of the spec.
- **`FR-D6`** is a functional requirement in the [PRD](../product/README.md). The letter is the requirement family. Each family has its own PRD section, and is specified in one place in the spec (spec §1.1).
- **`T-0.3`** is a Phase 0 acceptance test (spec §16).
- **`D2.4`** is a phased deliverable in [PRD §14.1](../product/prd/14-rollout-plan.md#141-phased-deliverables); each issue carries the `phase:N` label of the phase it belongs to.
- **`#123`** is a GitHub issue or pull request. Issues up to #285 were migrated from the old tracker; their bodies end with its id (`exemplar-…`) that older commits and PR descriptions name.

| Family | PRD area | PRD | Spec |
|---|---|---|---|
| FR-E | Engine: review every PR with org rules, one agent per rule, never block, repo context (E12–E14); rule selection and the per-repo inventory (E17–E18); presets, `extends` and project config (E19–E20) | §4.1, §7, §8.1–8.3 | §3.1, §5, §6.4–6.7, §6.12 |
| FR-V | Verification with model diversity | §8.4 | §6.8 |
| FR-T | Model output is untrusted and checked by code | §8.5 | §6.9, §13 |
| FR-P | Comment budget, labels, SARIF, dedupe | §8.6 | §6.10, §6.12 |
| FR-F | Validated one-click fixes | §8.7 | §6.11 |
| FR-B | Structured feedback | §9 | §9 |
| FR-A | Auto-approve behind deterministic gates | §12 | §6.13 |
| FR-C | PR lifecycle | §8.9 | §6.2 |
| FR-D | Degraded operation | §8.10 | §6.15 |
| FR-H | Data handling and eligibility | §8.11 | §13.1 |
| FR-S | Shift left: rule exports, pre-PR checks | §10 | §10 |
| FR-X | Evaluation before rollout | §11 | §8.8–8.9, §11 |
| FR-L | Learning plane: mining, extraction, ranking | §6 | §8 |

## Roadmap

Phases are from spec §16 and PRD §14.1. **Built** means implemented and tested in this repository; nothing has run against a real GitHub org, real model providers or production data yet, and every phase's exit criteria need those.

| Phase | Open work |
|---|---|
| 0 — Foundations | [`phase:0`](https://github.com/koblas/exemplar/issues?q=is%3Aopen+label%3Aphase%3A0) |
| 1 — Mine and curate | [`phase:1`](https://github.com/koblas/exemplar/issues?q=is%3Aopen+label%3Aphase%3A1) |
| 2 — Shadow | [`phase:2`](https://github.com/koblas/exemplar/issues?q=is%3Aopen+label%3Aphase%3A2) |
| 3 — Comment | [`phase:3`](https://github.com/koblas/exemplar/issues?q=is%3Aopen+label%3Aphase%3A3) |
| 4 — Shift left and bug lane | [`phase:4`](https://github.com/koblas/exemplar/issues?q=is%3Aopen+label%3Aphase%3A4) |
| 5 — Auto-approve (stays off in config) | [`phase:5`](https://github.com/koblas/exemplar/issues?q=is%3Aopen+label%3Aphase%3A5) |
| 6 — Platform | [`phase:6`](https://github.com/koblas/exemplar/issues?q=is%3Aopen+label%3Aphase%3A6) |
