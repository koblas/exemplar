# Product and design documents

The technical specification and the PRD, one file per top-level section. Edit them here.
Section numbers and requirement ids are unchanged from the single-file originals,
so a `§6.4.6` in a code comment or commit is in the spec's §6 file, and an `FR-E12`
is in the PRD section that `docs/plans/README.md` names for its family.
Headings inside each file keep their anchors.

## Technical specification

Cited as `§N.N`.

| § | Section | File |
|---|---|---|
| 0 | Front matter and change history | [`spec/00-front-matter.md`](spec/00-front-matter.md) |
| 1 | Basis | [`spec/01-basis.md`](spec/01-basis.md) |
| 2 | Architecture | [`spec/02-architecture.md`](spec/02-architecture.md) |
| 3 | Repositories and code layout | [`spec/03-repositories-and-code-layout.md`](spec/03-repositories-and-code-layout.md) |
| 4 | Data layer | [`spec/04-data-layer.md`](spec/04-data-layer.md) |
| 5 | GitHub integration | [`spec/05-github-integration.md`](spec/05-github-integration.md) |
| 6 | Review pipeline | [`spec/06-review-pipeline.md`](spec/06-review-pipeline.md) |
| 7 | Execution and durability | [`spec/07-execution-and-durability.md`](spec/07-execution-and-durability.md) |
| 8 | Learning plane | [`spec/08-learning-plane.md`](spec/08-learning-plane.md) |
| 9 | Feedback and rule lifecycle | [`spec/09-feedback-and-rule-lifecycle.md`](spec/09-feedback-and-rule-lifecycle.md) |
| 10 | Authoring-time delivery and other callers | [`spec/10-authoring-time-delivery-and-other-callers.md`](spec/10-authoring-time-delivery-and-other-callers.md) |
| 11 | Evaluation | [`spec/11-evaluation.md`](spec/11-evaluation.md) |
| 12 | Observability and dashboards | [`spec/12-observability-and-dashboards.md`](spec/12-observability-and-dashboards.md) |
| 13 | Security model | [`spec/13-security-model.md`](spec/13-security-model.md) |
| 14 | Technology stack | [`spec/14-technology-stack.md`](spec/14-technology-stack.md) |
| 15 | Operations runbook | [`spec/15-operations-runbook.md`](spec/15-operations-runbook.md) |
| 16 | Delivery plan | [`spec/16-delivery-plan.md`](spec/16-delivery-plan.md) |
| 17 | Required inputs | [`spec/17-required-inputs.md`](spec/17-required-inputs.md) |
| Appendix A | Example config files | [`spec/appendix-a-example-config-files.md`](spec/appendix-a-example-config-files.md) |
| Appendix B | References | [`spec/appendix-b-references.md`](spec/appendix-b-references.md) |
| Appendix C | Deferred: MCP server | [`spec/appendix-c-deferred-mcp-server.md`](spec/appendix-c-deferred-mcp-server.md) |

## PRD

Cited as `FR-…` ids and "PRD §N".

| § | Section | File |
|---|---|---|
| 0 | Front matter and change history | [`prd/00-front-matter.md`](prd/00-front-matter.md) |
| 1 | Summary | [`prd/01-summary.md`](prd/01-summary.md) |
| 2 | Problem Statement | [`prd/02-problem-statement.md`](prd/02-problem-statement.md) |
| 3 | Goals and Non-Goals | [`prd/03-goals-and-non-goals.md`](prd/03-goals-and-non-goals.md) |
| 4 | Users and Personas | [`prd/04-users-and-personas.md`](prd/04-users-and-personas.md) |
| 5 | System Overview | [`prd/05-system-overview.md`](prd/05-system-overview.md) |
| 6 | Functional Requirements — Learning Plane (offline) | [`prd/06-functional-requirements-learning-plane-offline.md`](prd/06-functional-requirements-learning-plane-offline.md) |
| 7 | Data Model — Rules | [`prd/07-data-model-rules.md`](prd/07-data-model-rules.md) |
| 8 | Functional Requirements — Enforcement Plane (online) | [`prd/08-functional-requirements-enforcement-plane-online.md`](prd/08-functional-requirements-enforcement-plane-online.md) |
| 9 | Feedback — structured, Tricorder-style — *revised* | [`prd/09-feedback-structured-tricorder-style-revised.md`](prd/09-feedback-structured-tricorder-style-revised.md) |
| 10 | Authoring-time delivery (shift left) — *new* | [`prd/10-authoring-time-delivery-shift-left-new.md`](prd/10-authoring-time-delivery-shift-left-new.md) |
| 11 | Evaluation — *revised* | [`prd/11-evaluation-revised.md`](prd/11-evaluation-revised.md) |
| 12 | Auto-Approve Gates | [`prd/12-auto-approve-gates.md`](prd/12-auto-approve-gates.md) |
| 13 | Success Metrics | [`prd/13-success-metrics.md`](prd/13-success-metrics.md) |
| 14 | Rollout Plan | [`prd/14-rollout-plan.md`](prd/14-rollout-plan.md) |
| 15 | Risks and Mitigations | [`prd/15-risks-and-mitigations.md`](prd/15-risks-and-mitigations.md) |
| 16 | Open Questions | [`prd/16-open-questions.md`](prd/16-open-questions.md) |
| 17 | Sample interactions | [`prd/17-sample-interactions.md`](prd/17-sample-interactions.md) |
| Appendix A | Design Principles | [`prd/appendix-a-design-principles.md`](prd/appendix-a-design-principles.md) |
| Appendix B | Prior art borrowed, and what we deliberately did not take | [`prd/appendix-b-prior-art-borrowed-and-what-we.md`](prd/appendix-b-prior-art-borrowed-and-what-we.md) |
