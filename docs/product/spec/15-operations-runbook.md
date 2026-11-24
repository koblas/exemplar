## 15. Operations runbook

| Situation | Action |
|---|---|
| Bot is noisy in one repo | `repos/<org>/<repo>.yaml: enabled: false` → merge (takes effect in seconds, ≤ 15 min worst case) |
| One rule is noisy everywhere | Set rule `status: shadow` → merge |
| Bad auto-approval suspected | `global.yaml: auto_approve: false` → merge; pull `approval_decisions` trace and transcript |
| Model outage | Runs become `incomplete` (§6.15): no comments, no approvals; the check run shows "Review did not complete" with a Re-run action; PRs proceed with human review as normal (nothing blocks) |
| Webhook backlog | Scale `exemplar-worker` (it runs the orchestrator role) ; sweeper catches up any missed PRs |
| Runs hitting the soft ceiling | Informational board shows which rules or PR shapes are expensive; narrow scope or lower the model tier for those rules; raise `soft_ceiling_usd` only if precision suffers from shedding |
| One model provider outage | Gateway fails over per stage; runs using a same-family verifier are flagged and cannot auto-approve; check failover rate on the informational board |
| All providers for a stage unavailable | Runs become incomplete: no comments, no approvals, check run shows "Review did not complete" with a Re-run action; re-run affected PRs when providers recover |
| A repo must stop being sent to providers immediately | Add it to `platform/eligibility.yaml` (Security) → merge; config cache invalidates on push; in-flight runs for that repo are cancelled |
| One bug class is noisy | `global.yaml: bug_lane.classes.<id>.enabled: false` → merge |
| Bad rule export landed in a repo | Revert the repo's export PR; set `export.agents: false` on the rule; next export run removes it everywhere |
| Feedback links failing | Slash commands and reactions continue to work; fix `exemplar-api` ; no data loss beyond the outage window |
| `exemplar-search` index behind or missing | Responses switch to degraded mode ( `git grep` , tighter caps); `bug.contract` skipped (no auto-approve); check fetch/index logs; delete the replica's volume to force a clean rebuild — the other replica keeps serving |
| Both `exemplar-search` replicas down | Context tools return errors the agents see; bug lane skipped; exemplar lane continues with the PR checkout only; reviews are not blocked |
| Reviews route to humans for "the bug lane did not run to full coverage" on an install without search | Expected on a repo with `extra_repos` (§6.4.6): add search, or drop `extra_repos` if the cross-repo context isn't needed. On a single-repository install it means the local references are not yet shipped; nothing is wrong |
| Search is off and someone thinks it's broken | `EXEMPLAR_SEARCH_URL` empty and the start-up warning mean off on purpose; a set URL with connection errors means missing. The check run says which |
| A PR keeps getting the same findings push after push | The round cap engaged (summary comment, `neutral` check); a human review resets it. If it engages often for one rule, the rule's fix or summary is unclear: flag it for the owner |
| Approval turned `pending` without a push | Base drift (§6.13): the base moved past `base_drift_commits` or touched the PR's paths; the gates re-run on the new merge result and re-approve if they pass. Nothing to do unless it stays pending, then check the merge-policy job |
| Runner quota saturated, queue age alert | Raise `launcher.max_runners` within the platform quota; check provider rate-limit headroom first, since runners waiting on 429s hold slots; a monorepo rebase storm drains on its own |
| A runner died mid-review | River retries the job in a new runner, which skips completed rule runs; the old claim expires. A review that keeps dying is incomplete after 3 attempts and says so on the check run |
| Scheduled job overlapping itself | The advisory lock makes the second run exit at once with a log line; no action |

---
