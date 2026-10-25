# How to respond to a finding

Exemplar posts one review per push, as comments, and never requests changes. Its `exemplar` check run says first whether the PR needs a human reviewer and, if so, why (a finding, a path outside the auto-approve list, a failed gate); [How a review works](../../concepts/how-a-review-works.md#who-reviews-this-pr) explains the outcomes. Each comment is a label and a message, a suggestion block when the fix was validated, a footer of feedback links, and a line naming the rule with a **why** link to its citation. What you do with it is recorded against the rule, so the bot gets quieter where it is wrong.

## Accept it

Press **Commit suggestion** on the suggestion block, or fix it yourself and push. Exemplar sees the fix on the next review and the thread resolves itself. If you want to say so explicitly:

```
/exemplar fixed
```

## Dismiss it

Reply in the thread with the reason that fits, or click the matching link in the footer (**Fixed**, **Wrong**, **Not important here**, **Already handled**, **Disagree with rule**):

| Reply | When |
|---|---|
| `/exemplar wrong <why>` | The finding is incorrect. Say why: the reason goes to the rule's owner, and a recurring one becomes a rule edit. |
| `/exemplar not-important` | Correct, but it doesn't matter here. Recurring ones on the same paths become a path exclusion the owner can merge. |
| `/exemplar handled` | Already dealt with elsewhere. |
| `/exemplar disagree <why>` | The rule itself is wrong for this code. |

Feedback is honoured from the PR's author, a member of the rule's owning team, or a human who reviewed or commented on the PR before the command. Anyone else gets a one-line reply and nothing is recorded. The links need you to be signed in to Exemplar with GitHub.

A comment that lists **Also in N other place(s)** stands for every place listed: one rule found the same problem in each. A reply, command, link or reaction on it applies to all of them, so dismiss it only if your reason holds for every place. Fix the places and push; the thread resolves once the last one is addressed.

A 👎 on the comment counts too, at half weight, and only against acceptance; a 👍 is recorded and counts for nothing. Resolving the thread alone is ambiguous and counts for neither. A plain reply is classified as agree, disagree or a question by a light model, and counts as such.

## Ignore it

A finding still posted with no signal when the PR merges is recorded as ignored. Ignored findings count against the rule's acceptance, so nothing is lost by saying nothing, but a reason helps its owner more.

## Security alerts

Security-category findings are also uploaded as code-scanning alerts. Dismissing the alert in GitHub's Security tab is feedback as well, and the alert closes on its own once the code no longer has the finding.

## What you will not see

- A "request changes" from Exemplar. It comments, or stays silent, or where a repository opts in, approves.
- Comments on naming, formatting or anything a linter in the repository already reports.
- More than the comment budget (seven per PR and three per file by default); the check run lists what was held back and why.
- A separate comment for each of many places one rule flags. More than three (by default) arrive as one comment listing the places, with no suggestion block.
- New inline comments push after push without end. After three pushes that each raised new findings, new ones (except blocking and Security) are listed in one "review rounds" comment instead, updated on each push. Ask a reviewer to look, or reply on the PR: a person's review or review comment starts the count again.
- A comment on a draft PR or a fork PR unless the repository opted in.

If a finding looks like it was injected (a comment that quotes your PR description back at you as a rule, say), it wasn't: rule ids, citations and severities come from `exemplar-config`, never from the model. Reply `/exemplar wrong` and tell the owner.
