# How to respond to an incident

Every switch is a file in `exemplar-config`: edit, open a PR, merge. A push to `exemplar-config` refreshes the workers within seconds (15 minutes at worst, the cache TTL). Rule owners and the platform team can also flip the per-rule and per-repo switches from the web UI, which opens and merges the same PR for them.

| Situation | Action |
|---|---|
| The bot is noisy in one repo | `repos/<org>/<repo>.yaml`: `enabled: false`, merge. |
| One rule is noisy everywhere | Set the rule's `status: shadow`, merge. Or **Shadow** on the rule's page in the web UI. |
| A bad auto-approval is suspected | `global.yaml`: `auto_approve: false`, merge. Or the repo's switch on the audit page, which turns it off for that repo at once. Pull the decision's trace from the audit page and the transcript. Turning it back on is a reviewed config PR, never one click. |
| One bug class is noisy | `global.yaml`: `bug_lane.classes.<id>.enabled: false`, merge. |
| A repo must stop being sent to providers now | Add it to `platform/eligibility.yaml` (Security owns it), merge. The config cache invalidates on push; in-flight runs for that repo are cancelled. |
| A bad rule export landed in a repo | Revert the repo's export PR; set `export.agents: false` on the rule; the next export run removes it everywhere. |
| Model outage | Runs become incomplete: no comments, no approvals; the check run shows "Review did not complete" with a **Re-run** action; PRs proceed with human review as normal, nothing blocks. Re-run affected PRs when the provider recovers. |
| One provider down | The gateway fails over per stage to the next provider in `global.yaml`. Runs whose verifier ends up in the same family as the generator are flagged and cannot auto-approve. Watch the failover rate on the operations board. |
| Every provider for a stage down | As a model outage: incomplete runs until a provider recovers. |
| Webhook backlog | Scale `exemplar-worker`; the `sweep_prs` job catches up any PR the backlog missed. The queue depth by repo is on the operations board. |
| Runs hitting the soft ceiling | The operations board shows which rules or PR shapes are expensive. Narrow their `file_patterns` or lower their `model_tier`; raise `budgets.soft_ceiling_usd` only if precision suffers from shedding. |
| Feedback links failing | Slash commands and reactions continue to work; fix `exemplar-api`. No data loss beyond the outage window. |
| `exemplar-search` index behind or missing | Responses switch to degraded mode (`git grep`, tighter caps); the `bug.contract` class is skipped (no auto-approve). Check the fetch and index logs; delete the replica's volume to force a clean rebuild, the other replica keeps serving. |
| Both `exemplar-search` replicas down | Context tools return errors the agents see; the bug lane is skipped; the exemplar lane continues with the PR checkout only; reviews are not blocked. |
| A credential leaked | [How to rotate keys](../install/rotate-keys.md). |

The alerts shipped with the Grafana boards cover the auto-approve cases: an auto-approved merge reverted or incident-linked within 28 days, auto-approved merges failing more often than human-approved ones, and an approval from a degraded or incomplete review ([Dashboards and alerts](../../reference/dashboards.md)).
