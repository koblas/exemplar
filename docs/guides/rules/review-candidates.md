# How to review mined candidates

The learning plane proposes rules it mined from your review history ([Rule lifecycle](../../concepts/rule-lifecycle.md#where-rules-come-from)). They reach you in two places. Rules you write yourself go through the same queue as owner seeds ([How to seed a rule by hand](seed-a-rule.md)).

## 1. The candidates page

`/ui/candidates` lists every candidate with its score, owner, the repositories it was mined from, and the comments it cites (shown only for repositories you can read). Open one to read the extracted constraint and the source comments.

- **Kill it** with a reason if it is wrong, already a linter rule, or not worth a rule. The reason is required: it teaches the extractor what not to propose. Only the candidate's owner or the platform team can kill it.
- Leave it for the `propose` job otherwise. It opens draft PRs for the top candidates, at most three open per owner team at a time, so the queue never floods you.

## 2. The draft PR in `exemplar-config`

Each proposed PR adds a rule file with its citations and an `evals/<rule>/cases.yaml` with positives from the source comments, negatives from CODEOWNER-approved PRs and injected violations. If the `compile` CronJob is enabled and the pattern worked, the rule arrives as `kind: compiled`.

1. Read the rule's prose as a prompt: tighten `what_to_flag`, add the false positives you can foresee as negative cases ([How to write eval cases](eval-cases.md)).
2. Check `file_patterns` and `owner`.
3. Run it on a live PR with `exemplar try` or the playground ([How to test a rule before merging](test-a-rule.md)).
4. Let config CI's `eval`, `volume` and `linter-overlap` jobs speak.
5. Merge with `status: shadow`; promote later.

To kill it from the PR instead, add the `killed` label, comment why, and close it. The next `propose` run kills the candidate with your latest comment as the reason, and it stops counting against your team's three open PRs. A PR closed without the label or without a comment from a person is reopened with a comment asking for both: a candidate dropped without a reason would only be mined and proposed again.

## 3. The weekly digest

`digest` opens one issue per owner team per week in `exemplar-config` with each of your rules' acceptance, eFP and dismissal reasons, from the same views as the Grafana boards, so you see which rules need attention before demotion does it for you.
