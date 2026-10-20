# How to adjudicate feedback

When an author dismisses a finding as `wrong` or `disagree`, the rule's owners decide whether the finding or the author was right. Adjudications carry the highest weight in a rule's precision and are one of the two ways a rule can be auto-demoted ([Rule lifecycle](../../concepts/rule-lifecycle.md#feedback-and-demotion)).

## 1. Find what needs a verdict

- The `adjudication` Grafana board shows the owner queue per team: findings with an author `wrong` or `disagree` and no adjudication, with the oldest age.
- The rule's page in the web UI lists its recent findings with their state and events.
- The weekly digest issue in `exemplar-config` lists your rules' dismissals.

`feedback.owner_response_days` in `global.yaml` is how long a dismissal waits for an owner before the demotion logic counts it alone.

## 2. Adjudicate in the thread

Reply in the finding's review thread on the PR:

```
/exemplar uphold the body is still unclosed on the error path
```

```
/exemplar overturn the helper closes it; add that to what_to_flag
```

Only members of the rule's owning team, other than the PR's author, can uphold or overturn; membership is checked live, and anyone else gets a one-line reply and no event. [Feedback commands](../../reference/feedback-commands.md) lists every command.

## 3. Act on the pattern

- **Overturned repeatedly on the same paths:** the `teach` job opens a draft config PR excluding those paths once "not important" dismissals cluster there (three in 30 days, or once from you). Merge it, or write a better exclusion.
- **Overturned because the rule is wrong:** edit `what_to_flag`, add the case as a negative, and open a PR. The owner review issue `teach` opens for `rule_wrong` and `disagree` classes is the place to record it.
- **Upheld but ignored:** the rule may be right and unloved. Consider `severity`, or a fix policy that makes it cheap to accept.
- **Noisy beyond repair:** **Shadow** or **Kill** on the rule's page opens and merges a config PR in your name; the audit of what you did is at `/ui/actions`.

A rule whose eFP stays over `feedback.efp_bar` with a precision lower bound under `feedback.demotion_wilson_floor`, and whose negatives include your adjudication or two non-author people, is moved to shadow by the nightly `demote` job, naming you in the PR.
