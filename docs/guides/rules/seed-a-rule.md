# How to seed a rule by hand

When you already know a rule your team wants, write it yourself instead of waiting for the learning plane to mine it. A new rule saved as `status: candidate` is an *owner seed*: Exemplar records it as a candidate, generates eval cases for it, and promotes it once you merge it as `shadow` or `active` ([Rule lifecycle](../../concepts/rule-lifecycle.md#where-rules-come-from)). Every field is on [rules/**/*.yaml](../../reference/config/rule.md); [How to write a rule](write-a-rule.md) covers the decisions.

## 1. Write the rule and open the PR

=== "From the playground"

    1. Open `/ui/playground`, paste the rule (or start from a run with `?from=<id>`), and run it on a PR you can read ([How to test a rule before merging](test-a-rule.md)).
    2. When the findings look right, choose **Save as draft PR**. A rule with a new `id` becomes a draft PR in `exemplar-config` with `status: candidate`, whatever the draft said, and you are recorded as its author.

=== "In exemplar-config"

    1. On a branch of `exemplar-config` itself (not a fork: a fork's PR is skipped), add `rules/<category>/<id>.yaml` with `status: candidate`.
    2. Link the design doc or incident in `citation.doc`; it becomes the finding's **why**.
    3. Open the PR. The next `propose` run records the seed, with the PR's author as its author.

Only a rule file the PR *adds* is a seed. Editing an existing rule is a normal config PR. A rule id is a seed on one PR at a time: while its PR is open, saving the same id again from the playground is refused, and another PR adding it is not recorded. Change the rule on its PR instead.

## 2. Let Exemplar generate eval cases

The next `evals` run generates cases for the seed: negatives from recent CODEOWNER-approved PRs in eligible repositories that touch its `file_patterns`, and violations injected into them as positives. The `propose` run after it delivers them:

- on a playground draft PR, it commits `evals/<id>/cases.yaml` and the patches;
- on your own branch, it posts them as a PR comment for you to commit, edited as you see fit.

If the PR already has `evals/<id>/cases.yaml`, your cases are kept and nothing is generated onto the PR. Cases are generated again when you change the rule before they are delivered, or open a new PR for it. Review the synthetic cases and add the false positives you foresee ([How to write eval cases](eval-cases.md)).

## 3. Merge it through the gate

1. Set `status: shadow` (or `active`) when you are ready.
2. Let config CI's checks pass, as for any rule.
3. Merge. The next `propose` run marks the candidate promoted.

Close the PR without merging and the seed is retired. To drop it while the PR stays open, kill it on `/ui/candidates` with a reason ([How to review mined candidates](review-candidates.md)).
