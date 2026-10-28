# Glossary

The words the rest of this site uses. Terms are in alphabetical order.

**Acceptance**
: How often a rule's posted findings were acted on (fixed) or agreed with, compared with how often they were dismissed or ignored. A 👎 reaction counts against acceptance at half weight.

**Active**
: A rule `status` whose findings are posted on PRs. See [Rule lifecycle](rule-lifecycle.md).

**Adjudication**
: A rule owner's verdict on the feedback a finding got: `uphold` (the finding was right) or `overturn` (it was wrong). A rule cannot be auto-demoted without one, plus two people other than the author. See [Feedback commands](../reference/feedback-commands.md).

**Adversarial set**
: A set of prompt-injection test cases in `evals/_adversarial`, run on every config change. No case may produce an approval, hide a true finding, make up a citation, or claim false coverage.

**Approval mode**
: How a repository's branch rules count Exemplar's approval. In `native_approve`, Exemplar's `APPROVE` counts toward the required approvals. In `merge_policy_check`, the repo instead requires the `exemplar/merge-policy` check, and Exemplar reports whether a person with write access approved.

**Auto-approve**
: Exemplar's own `APPROVE` on a low-risk PR, given only after every deterministic gate (Stage 1) and the two-model risk check (Stage 2) passed. Off unless both `global.yaml` and the repo opt in.

**Baseline reviewer**
: A generic review with no rules, run in shadow on a sample of PRs so Exemplar's precision can be compared against it. Its findings are never posted.

**Bug lane** (Lane 3)
: Agents that look for common bug classes without any rule telling them to, on risky PRs and on every auto-approve candidate.

**Candidate**
: A proposed rule, either mined by the learning plane or saved from the playground, that has not been merged yet. Shown on the candidates page with its citations.

**Check run**
: The `exemplar` check that GitHub shows on every reviewed PR head. It is neutral when the review completed, showing the cost and the rules that did not run. It is neutral with a **Re-run** action when the review was incomplete. The separate `exemplar/merge-policy` check is the one a `merge_policy_check` repo requires.

**Compiled rule** (Lane 1)
: A rule with `kind: compiled`. It runs as an ast-grep or Semgrep pattern, with no model call.

**Config CI**
: The GitHub Actions workflows in `exemplar-config` (`eval.yml`, `nightly.yml`, `export-approval.yml`) that check every config PR before it can merge.

**Coverage**
: The share of a PR's in-scope changes that the review actually examined. Any coverage below 1.0 makes the PR ineligible for auto-approve.

**Degraded run**
: A review in which a stage timed out or a provider failed over, so optional work was dropped. A degraded run can post comments but never approves.

**Demotion**
: Moving an active rule back to shadow because people keep rejecting its findings. Happens automatically, nightly, through a config PR that merges on its own.

**Deterministic**
: Decided by code with fixed rules, so the same input always gives the same answer. The opposite of a model's judgment, which can vary. Auto-approve gates are deterministic.

**Effective false-positive rate (eFP)**
: The weighted share of a rule's judged findings that were dismissed as wrong or not important, with each dismissal weighted by the role of the person who made it. Every active rule must stay under `feedback.efp_bar`.

**Eligibility**
: Whether a repository may be reviewed, mined, indexed or sent to a provider at all. Security's denylist in `platform/eligibility.yaml` cannot be overridden.

**Eval cases**
: Labelled positive and negative examples for a rule in `evals/<rule>/cases.yaml`. A case is historical (a real commit) or synthetic (a patch). Config CI fails a rule that scores below `eval_thresholds`.

**Exemplar rule** (Lane 2)
: A rule with `kind: exemplar`. It gets one AI agent per rule, with the rule's text and read-only repository tools.

**exemplar-config**
: The git repository your teams own that holds every rule, repo file, platform file and eval case. The GitHub App is registered on its owner. Workers clone it with git-sync and refresh on every push.

**Export**
: Writing the summaries of active rules into coding-agent instruction files (`AGENTS.md`, `CLAUDE.md`, Copilot, Cursor) in each opted-in repository. Done by a separate GitHub App.

**Feedback link**
: A signed link in a comment's footer (Fixed, Wrong, Not important here, Already handled, Disagree with rule) that records feedback for a signed-in GitHub identity.

**Finding**
: One thing a lane raised on one PR head. It has a rule, an anchor (the lines it points at), a message, an optional fix, a verifier verdict and a state (posted, shadow, or suppressed with a reason).

**Fix policy**
: What a rule's finding may carry: `none`, `describe` (a fix in prose), or `suggest` (a GitHub suggestion block, posted only when the fix applied and parsed).

**Gateway**
: `exemplar-gateway`, the only process holding provider credentials. It routes each stage through an ordered provider list with failover and records cost.

**Head**
: The exact commit at the tip of a PR's branch. Every push creates a new head, and a review is tied to one head.

**Incomplete run**
: A review that could not finish: a stage with no working provider, a diff that could not be redacted, or a runner that failed on every retry. Nothing more is posted, and the check run offers a re-run.

**Kill switch**
: Any of the ways to stop Exemplar quickly: `enabled: false` in `global.yaml` or a repo file, `auto_approve: false`, a rule's `status`, or the per-repo switch on the audit page. See [How to respond to an incident](../guides/operate/runbook.md).

**Lane**
: One of the three ways a finding is produced: compiled rules, exemplar agents, the bug lane.

**Learning plane**
: The scheduled jobs that mine review history into candidates and act on feedback: `exemplar-learn` (Go) and the `embed`, `cluster` and `rank` jobs (Python).

**Linter overlap**
: A finding that duplicates an annotation from one of the repo's configured linters. It is dropped. A config CI check also refuses a rule whose positive cases a linter already flags.

**Merge-safety score**
: A risk score for a PR computed by code from incident and revert history, churn, missing tests, sensitive paths, a new author and force pushes, weighted by `merge_safety.weights`.

**Model family**
: A group of models from one maker, such as all Claude models. Exemplar prefers the verifier to come from a different family than the generator, so one model's blind spots don't carry over.

**Owner**
: The team (`@org/team`) named in a rule's `owner`. Its members adjudicate feedback, move the rule between statuses, and receive its digest.

**Playground**
: The web UI page where a draft rule runs alone on a PR the viewer can read. It runs in the worker and posts nothing.

**Pseudonym**
: The keyed hash that replaces an author's identity in mined review comments.

**Review-by**
: A rule's expiry date. Once it passes, the expiry job either extends the rule or moves it to shadow.

**River**
: The Postgres-backed job queue that every background step runs on. There is no other message broker.

**Root key**
: The one Secret (`exemplar-root-key`) from which the link, session, gateway, search, pseudonym and runner-claim keys are derived.

**Same-family**
: A finding whose verifier came from the same model family as its generator. It is posted, but blocks auto-approve.

**Setup mode**
: What `exemplar-api` runs until the `exemplar-github-app` Secret exists: a page reached over a port-forward that registers the GitHub App from a manifest.

**Shadow**
: Reviewing without writing anything to the PR. Applies to a whole repository (`shadow: true`) or to one rule (`status: shadow`).

**Stage 1 / Stage 2**
: The two halves of an auto-approve decision: deterministic gates first, then a two-model risk check plus the merge-safety score.

**Teach the bot**
: The nightly job that turns recurring rejections into a draft path exclusion or an owner review issue.

**Verdict**
: The outcome of a review for the question "who reviews this PR?": approved with no human review needed, or routed to humans (with findings, or with nothing to flag but a gate that failed). Recorded in `approval_decisions`. See [How a review works](how-a-review-works.md#who-reviews-this-pr).

**Verifier**
: The second model that tries to refute each finding and scores how much it matters, before anything is posted.
