# Your first review

You have an installation that passes `/ui/setup` ([Quickstart](quickstart.md)) and one repository in `exemplar-config` with `shadow: true`. In this tutorial you watch a shadow review, turn posting on, and give the bot its first feedback. Fifteen minutes, plus however long your pull request takes to review.

## 1. Watch a shadow review

Open a pull request in the shadow repo. The worker reviews it and stores the result, but posts nothing: no comments, no check run, no approval.

In the web UI, open `https://<your host>/ui/` and sign in. The rules page lists every rule with its health over the last weeks; open a rule to see its recent findings, including the ones from your shadow PR. Open a finding to see the message, the lines it anchors to, the verifier's verdict and what the comment would have said.

If nothing arrives, `/ui/setup` says whether the webhook delivery reached the cluster and whether the event was stored. [How to troubleshoot an installation](../guides/operate/troubleshoot.md) has the next steps.

## 2. Turn posting on

When the findings look right, set `shadow: false` in that repo's file in `exemplar-config` and merge. A push to `exemplar-config` refreshes the workers' config at once; the cache TTL is only a backstop.

The next push to the pull request gets the `exemplar` check run, whose title says whether a human needs to review the PR and whose first line says why, and one review with inline comments. Each comment carries the rule id and severity, a suggestion when the fix is local, and a footer of feedback links. Exemplar never requests changes: it comments and stays neutral, or, where a repo opts in to auto-approve, approves a PR that passes every gate so no human has to read it. Until you opt a repository in, every PR is routed to your human reviewers; [How a review works](../concepts/how-a-review-works.md#who-reviews-this-pr) describes the three outcomes.

## 3. Give feedback

On a posted finding, reply in its thread:

```
/exemplar wrong the context is already passed two lines up
```

or click **Wrong** in the comment's footer. Either way the finding's page in the web UI shows the event within a minute, with your role on the PR (author, reviewer, owner).

Feedback is what the learning plane runs on. A rule people keep marking wrong is demoted to shadow automatically; a correction in a reply becomes a proposed rule edit for its owner. [How to respond to a finding](../guides/authors/respond-to-a-finding.md) lists every command, and [Rule lifecycle](../concepts/rule-lifecycle.md) explains what the feedback does.

## Next

- [Your first rule](first-rule.md): write a rule of your own and see it run.
- [How to install for production](../guides/install/production.md): a managed Postgres, two replicas and the edge rules.
