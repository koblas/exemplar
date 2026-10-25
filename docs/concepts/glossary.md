# Glossary

**Acceptance**
: The share of a rule's posted findings that authors acted on (fixed) or agreed with, against those dismissed or ignored. A 👎 reaction counts against acceptance at half weight.

**Active**
: A rule `status` whose findings are posted. See [Rule lifecycle](rule-lifecycle.md).

**Adjudication**
: An owner's verdict on a finding's feedback: `uphold` (the finding was right) or `overturn` (it was wrong). Needed before a rule can be auto-demoted, together with two non-author actors. See [Feedback commands](../reference/feedback-commands.md).

**Adversarial set**
: The prompt-injection cases in `evals/_adversarial`, run on every config change. No case may produce an approval, a suppressed true finding, a fabricated citation or false coverage.

**Approval mode**
: How a repository's branch rules count Exemplar's approval: `native_approve` (Exemplar's `APPROVE` counts toward required approvals) or `merge_policy_check` (the repo requires the `exemplar/merge-policy` check instead, and Exemplar reports whether a person with write access approved).

**Auto-approve**
: Exemplar's native `APPROVE` on a low-risk PR after every deterministic gate (Stage 1) and the two-model categorisation (Stage 2) passed. Off unless `global.yaml` and the repo both opt in.

**Baseline reviewer**
: A generic, rule-free review run in shadow on a sample of PRs so Exemplar's precision can be compared with it. Its findings are never posted.

**Bug lane** (Lane 3)
: Agents that look for common bug classes independent of any rule, on risky PRs and on every auto-approve candidate.

**Candidate**
: A proposed rule, mined by the learning plane or saved from the playground, not yet merged. Visible on the candidates page with its citations.

**Check run**
: The `exemplar` check GitHub shows on every reviewed head: neutral when complete, with the cost and the rules that did not run; neutral with a **Re-run** action when incomplete. The separate `exemplar/merge-policy` check is the one a `merge_policy_check` repo requires.

**Compiled rule** (Lane 1)
: A rule with `kind: compiled` that runs as an ast-grep or Semgrep pattern, with no model call.

**Config CI**
: The GitHub Actions workflows in `exemplar-config` (`eval.yml`, `nightly.yml`, `export-approval.yml`) that validate every config PR.

**Coverage**
: The share of a PR's in-scope changes the review actually examined. Any coverage below 1.0 makes the PR ineligible for auto-approve.

**Degraded run**
: A review in which a stage timed out or a provider failed over, so optional work was dropped. A degraded run can post comments but never approves.

**Demotion**
: Moving an active rule back to shadow because people keep rejecting it. Automatic, nightly, by a config PR that merges on its own.

**Effective false-positive rate (eFP)**
: The weighted share of a rule's judged findings dismissed as wrong or not important, by actors' roles. Every active rule must stay under `feedback.efp_bar`.

**Eligibility**
: Whether a repository may be reviewed, mined, indexed or sent to a provider at all. Security's denylist in `platform/eligibility.yaml` is absolute.

**Eval cases**
: Labelled positive and negative examples for a rule in `evals/<rule>/cases.yaml`, historical (a commit) or synthetic (a patch). Config CI fails a rule below `eval_thresholds`.

**Exemplar rule** (Lane 2)
: A rule with `kind: exemplar`: one agent per rule, with the rule's text and read-only repository tools.

**exemplar-config**
: The git repository your teams own that holds every rule, repo file, platform file and eval case. The App is registered on its owner. Workers clone it with git-sync and refresh on every push.

**Export**
: The rendering of active rules' summaries into coding-agent instruction files (`AGENTS.md`, `CLAUDE.md`, Copilot, Cursor) in each opted-in repository, by a separate GitHub App.

**Feedback link**
: A signed link in a comment's footer (Fixed, Wrong, Not important here, Already handled, Disagree with rule) that records feedback for a signed-in GitHub identity.

**Finding**
: One thing a lane raised on one PR head: a rule, an anchor, a message, an optional fix, a verifier verdict and a state (posted, shadow, suppressed with a reason).

**Fix policy**
: What a rule's finding may carry: `none`, `describe` (prose), or `suggest` (a GitHub suggestion block, posted only when the fix applied and parsed).

**Gateway**
: `exemplar-gateway`, the only process holding provider credentials. It routes each stage through an ordered provider list with failover and records cost.

**Incomplete run**
: A review that could not finish (a stage with no working provider, a diff that could not be redacted, a runner that failed on every retry). Nothing more is posted; the check run offers a re-run.

**Kill switch**
: The ways to stop Exemplar quickly: `enabled: false` in `global.yaml` or a repo file, `auto_approve: false`, a rule's `status`, or the per-repo switch on the audit page. See [How to respond to an incident](../guides/operate/runbook.md).

**Lane**
: One of the three ways a finding is produced: compiled rules, exemplar agents, the bug lane.

**Learning plane**
: The CronJobs that mine review history into candidates and act on feedback: `exemplar-learn` (Go) and the `cluster` and `rank` jobs (Python).

**Linter overlap**
: A finding that duplicates an annotation from one of the repo's configured linters; dropped, and a config CI check refuses a rule whose positive cases a linter already flags.

**Merge-safety score**
: A deterministic risk score for a PR from incident and revert history, churn, missing tests, sensitive paths, a new author and force pushes, weighted by `merge_safety.weights`.

**Owner**
: The team (`@org/team`) named in a rule's `owner`. Its members adjudicate feedback, move the rule, and receive its digest.

**Playground**
: The web UI page where a draft rule runs alone on a PR the viewer can read, in the worker, posting nothing.

**Pseudonym**
: The keyed hash that replaces an author's identity in mined review comments.

**Review-by**
: A rule's expiry date. Past it, the rule is extended or moved to shadow by the expiry job.

**River**
: The Postgres-backed job queue every asynchronous step runs on. There is no other broker.

**Root key**
: The one Secret (`exemplar-root-key`) from which the link, session, gateway, search, pseudonym and runner-claim keys derive.

**Same-family**
: A finding whose verifier was from the same model family as its generator. Posted, but blocks auto-approve.

**Setup mode**
: What `exemplar-api` runs until the `exemplar-github-app` Secret exists: a page on a port-forward that registers the GitHub App from a manifest.

**Shadow**
: Reviewing without writing to the PR. A whole repository (`shadow: true`) or one rule (`status: shadow`).

**Stage 1 / Stage 2**
: The two halves of an auto-approve decision: deterministic gates, then a two-model categorisation plus the merge-safety score.

**Teach the bot**
: The nightly job that turns recurring rejections into a draft path exclusion or an owner review issue.

**Verdict**
: The outcome of a review for the question "who reviews this PR?": approved with no human review needed, or routed to humans (with findings, or with nothing to flag but a gate that failed). Recorded in `approval_decisions`. See [How a review works](how-a-review-works.md#who-reviews-this-pr).

**Verifier**
: The second model that tries to refute each finding and scores its importance before anything is posted.
