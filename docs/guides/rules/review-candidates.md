# How to review mined candidates

The learning plane proposes rules it mined from your review history ([Rule lifecycle](../../concepts/rule-lifecycle.md#where-rules-come-from)). They reach you in two places.

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

Closing the PR without merging is a signal too: the candidate goes back to the pool.

## 3. The weekly digest

`digest` opens one issue per owner team per week in `exemplar-config` with each of your rules' acceptance, eFP and dismissal reasons, from the same views as the Grafana boards, so you see which rules need attention before demotion does it for you.
