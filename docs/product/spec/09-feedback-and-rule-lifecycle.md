## 9. Feedback and rule lifecycle

### 9.1 Signals

| Signal | Detection | Event kind |
|---|---|---|
| Explicit reason (link or slash command) | `exemplar-api /f/...` or `/exemplar …` reply | `fb_fixed` , `fb_wrong` , `fb_not_important` , `fb_handled` , `fb_disagree` (+ free text) |
| Applied suggestion | Next commit contains the suggested text at the anchor | `applied_suggestion` |
| Fixed without suggestion | Anchored lines changed in a later commit; light model confirms the change addresses the finding | `fixed` |
| Reply sentiment | `pull_request_review_comment` in thread, classified agree/disagree/question | `reply_agree` , `reply_disagree` , `reply_question` |
| Reaction | One GraphQL query per poll (every 30 min) fetching reactions for all Exemplar comments on PRs updated in 24 h via node ids, plus on close | `react_up` , `react_down` (secondary: weight 0.5, outside eFP — §9.2) |
| Code-scanning dismissal | `code_scanning_alert.closed_by_user` | mapped to `fb_wrong` / `fb_not_important` |
| Thread resolved | `pull_request_review_thread.resolved` | `resolved` (ambiguous alone) |
| Ignored | PR merged with the finding untouched and no other signal | `ignored` |
| Human duplicate | Finding withheld as `human_duplicate` (§6.10) | `human_agreed` |
| Human independently raised | A human comment posted later on overlapping lines of a finding (posted, shadow or suppressed) is judged the same concern by the light model | `human_agreed` |
| Owner adjudication | Rule owner replies `/exemplar uphold` or `/exemplar overturn` on a thread, or uses the digest link | `adjudicated_uphold` , `adjudicated_overturn` |

**As implemented** (feedback signals):
- **Thread resolution:** a person resolving an Exemplar thread records `resolved` (author, owner, else reviewer). Exemplar's own resolutions are ignored.
- **Replies:** a reply that is not a command is classified by the light model (`reply-classifier-v1`): agree, disagree, question or other. Other records nothing.
- **Raised independently:** a later review comment counts only from an owner, member or collaborator who isn't the PR author. It is compared with the PR's production findings on overlapping lines (posted, shadow or suppressed; at most 5 judged), using the human-comment judge.
- **Ignored:** on merge, each finding still posted with no signal at all records the author's `ignored`. Reactions are polled first.
- **Fixed without suggestion:** when a push closes a finding as addressed and its suggestion was not applied, the light model (`fixed-judge-v1`) compares the stored anchored code with the code there now (±3 lines), at most 10 per review. A yes records the author's `fixed`.
- **Reactions:**
  - **When polled:** every 30 minutes for PRs with a production review finished in the last 24 hours. The installation comes from the repo's latest delivery.
  - **Weights:** `feedback.reaction_weight` (0.5) for the author, owner or a participant; 0 for an observer.
  - **Excluded:** bot reactors, and reactions other than 👍/👎.
  - **Removal:** a reaction removed later stays recorded.

The bot's own seeded reactions are excluded by actor id (FR-T4). Each event records the actor's **role** on that PR, derived by code from PR participation at the moment of the event: `author`, `reviewer` (any other human who submitted a review or comment on the PR before the event), `owner` (a member of the rule's owning team, checked against the GitHub Teams API), or `observer` (anyone else — weight 0, audit only). A link click or dismissal never grants `reviewer` on its own.

**Precedence.** When one actor leaves conflicting events on a finding (Wrong, then Fixed), the latest explicit event from that actor supersedes their earlier ones; an owner adjudication supersedes everything else on the finding. `finding_outcomes` applies this before the boolean roll-ups, so a finding is never both accepted and rejected.

### 9.2 Definitions

- **Accepted** = `applied_suggestion` OR `fb_fixed` OR `fixed`.
- **Rejected** = any `fb_wrong`, `fb_not_important`, `fb_handled`, `fb_disagree`, or `react_down`.
- **Ignored** = `ignored`.
- **Acceptance rate** = accepted ÷ (accepted + rejected + ignored), over posted findings. Findings withheld as human duplicates are not in this rate (no one saw them).
- **Effective false-positive rate (eFP), role-weighted** (FR-B7). Negatives are `fb_wrong`, `fb_not_important`, `fb_disagree`; positives are accepted findings and `human_agreed`. Reactions are secondary signals (PRD §9): `react_down` counts as a rejection for the acceptance rate at weight 0.5 but does not enter eFP, and `react_up` is reported but counts for nothing. Each event's weight is its actor's role weight — author 1, reviewer 2, owner 4. If an owner has adjudicated a finding, only the adjudication counts for it (owner weight; `uphold` = positive, `overturn` = negative). `eFP = Σ w·negative ÷ Σ w·(positive + negative)`
- **Rule precision** (used for ranking, shedding and demotion) = positives ÷ (positives + negatives), unweighted counts of findings, including `human_agreed` (FR-B8). Reactions are excluded.
- **Severity-weighted acceptance** (PRD G1, §13, v0.9) = Σ w(severity) over accepted ÷ Σ w(severity) over accepted + rejected + ignored, with `blocking` 3, `important` 2, `suggestion` 1, over posted findings. This is the goal metric and the Phase 3 and 5 exit criterion; the unweighted acceptance rate stays as an informational metric for comparability with other tools' figures.
- **Ignored rate** = ignored ÷ posted; reported alongside, since a rising ignored rate with flat eFP means the bot is being tuned out.
- `fb_handled` is a rejection for acceptance purposes and a context gap for routing (§9.3), not an eFP negative.

Human-comment baselines use the same definitions on human review comments.

### 9.3 Routing by reason

| Reason | Goes to | Automated action |
|---|---|---|
| Wrong | Verifier and rule prompt | Add to `verifier_labels` (an author's as `author_signal`, anyone else's as `human_review`; an owner's uphold/overturn is a `human_review` label `true`/`wrong` that replaces author signals); if per-rule wrong-rate > 15%, flag prompt for owner |
| Not important here | Scope and importance | Path clustering of dismissals → importance-prior proposal (§8.11) or, when one rule's findings cluster under one prefix, a scope proposal (§8.7) |
| Already handled | Context providers | Logged with free text; weekly review by the platform team for missing context (e.g. wrappers outside the default `dependents` scope) |
| Disagree with rule | Rule owner | Counted per rule; ≥ 3 in 30 days opens an owner review issue |

### 9.4 Demotion and owner accountability

- **Flagging.** Any author `fb_wrong` or `fb_disagree` adds the finding to the owner's adjudication queue (digest and web UI). Author signals alone **flag** a rule; they never demote it.
- **Demotion.** Nightly, per rule and per bug class, over the last 30 findings with a judgment: demote when **both** hold: the weighted eFP exceeds the bar (0.10), and the Wilson lower bound of unweighted rule precision over those 30 findings is below 0.80 (Wilson is defined on counts, so the bound uses counts and the role weighting is applied in the eFP test) **and** the negatives include either an owner adjudication or explicit signals from at least two distinct non-author actors. The bot opens **and auto-merges** a config PR setting `status: shadow` and notifies the owner.
  - **Counting:** a judged finding is a negative when its negative weight outweighs its positive weight (`finding_outcomes`). The Wilson bound is 95%.
  - **Minimum:** demotion needs the full 30 judged findings. Fewer only flag.
  - **Non-author signals:** they must be explicit (wrong, not important, disagree); reactions don't count.
  - **Where it runs:** `exemplar-learn demote`, nightly, with the config-writer token, which never reaches agent workers (§5).
  - **Scope:** only global rules in `exemplar-config` are demoted. A repo's own rules and bug classes are reported and skipped.
  - **Merging:** the PR mentions the owner. If branch protection refuses the merge, it stays open for a human.
  - **Repeats:** there is one demotion branch per rule version, so a re-promoted rule can be demoted again.
- **Owner response.** A flagged rule whose owner takes no action (merge, comment or close on the flag issue) within 14 days is demoted the same way.
  - **When a rule is flagged:** both bars are crossed but the negatives come from authors alone.
  - **The flag issue:** `exemplar-learn demote` opens one per rule version in `exemplar-config` (label `exemplar-flag`), naming the owner and the deadline.
  - **Response:** any comment by a person, or closing the issue, counts.
  - **No response:** after `feedback.owner_response_days` (14) the rule is demoted by the usual PR and the issue is closed with a link.
- Demotion only reduces behavior; **promotion always requires a human**.
- **Weekly digest** per owner team (PRD §17.8): opened as an issue in `exemplar-config` (or posted to Slack), generated from the same SQL views as the dashboards, including the adjudication queue.
  - **Implementation:** `exemplar-learn digest`, Mondays. It opens one issue per owner and week (label `exemplar-digest`).
  - **Contents:**
    - per rule over 28 days: posted, accepted and weighted eFP;
    - a health column: review date passed, being demoted, flagged, over the bar, close to it (within 5 points) or healthy;
    - the top dismissal reason per rule, and where it clusters;
    - the findings waiting for adjudication, linked to their threads;
    - the PRs auto-approved in the last week whose touched paths the owner holds in CODEOWNERS (FR-A5, from the Stage 2 trace, which now records every touched path's owners).
