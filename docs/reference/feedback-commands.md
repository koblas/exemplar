# Feedback commands

Every way a person can tell Exemplar what they thought of a finding, and what each records in `feedback_events`. The kinds feed each rule's acceptance and effective false-positive rate ([Rule lifecycle](../concepts/rule-lifecycle.md#feedback-and-demotion)).

## Slash commands

A reply `/exemplar <command> [reason]` in an Exemplar review thread records an event on that thread's finding. The first word after `/exemplar` is the command, case-insensitive; the rest is the reason.

| Command | Records | Who may use it |
|---|---|---|
| `/exemplar fixed` | `fb_fixed`: the author fixed it. | Author, reviewers, owners |
| `/exemplar wrong <reason>` | `fb_wrong`: the finding is incorrect. | Author, reviewers, owners |
| `/exemplar not-important` (or `not_important`) | `fb_not_important`: correct but doesn't matter here. | Author, reviewers, owners |
| `/exemplar handled` | `fb_handled`: already dealt with elsewhere. | Author, reviewers, owners |
| `/exemplar disagree <reason>` | `fb_disagree`: the rule is wrong for this code. | Author, reviewers, owners |
| `/exemplar uphold [reason]` | `adjudicated_uphold`: the owner confirms the finding. | Members of the rule's owning team, other than the PR's author |
| `/exemplar overturn [reason]` | `adjudicated_overturn`: the owner rejects the finding. | Members of the rule's owning team, other than the PR's author |

"Reviewers" are humans who reviewed or commented on the PR before the command. Team membership for `uphold` and `overturn` is checked live. A command from anyone else, or an unknown command, gets a one-line reply and no event:

- unknown: "Exemplar didn't recognize that command. Use `/exemplar fixed`, `wrong <reason>`, `not-important`, `handled` or `disagree`; rule owners can also `uphold` or `overturn`."
- not allowed: "Only this pull request's author, its reviewers and the rule's owners can give Exemplar feedback, so this wasn't recorded."
- not an owner: "Only members of the rule's owning team, other than this pull request's author, can uphold or overturn a finding, so this wasn't recorded."

## Feedback links

The footer of every posted comment carries one signed link per reason: **Fixed**, **Wrong**, **Not important here**, **Already handled**, **Disagree with rule**, recording the same kinds as the commands. The person must be signed in to exemplar-api with a GitHub identity; the role comes from their participation in the PR before the click, and a click never makes someone a reviewer on its own. Anyone else is an observer.

## Reactions

👍 and 👎 on Exemplar's comments are polled every 30 minutes for PRs with recent reviews, and once more when a PR merges. A 👎 rejects for acceptance at weight 0.5 but is outside eFP; a 👍 is reported and counts for nothing. The bot's own reactions are excluded, and a reaction removed later stays recorded.

## Observed signals

Recorded without a command, from webhooks:

| Signal | Records | Counts toward |
|---|---|---|
| Someone resolved an Exemplar thread | `resolved` | Neither acceptance nor eFP (ambiguous alone) |
| A reply in an Exemplar thread that is not a command | `agree`, `disagree` or `question`, classified by the light model | As the class says |
| A later human review comment overlapping a finding's lines (posted, shadow or suppressed) that the light model judges the same concern | `human_agreed` | Acceptance; only trusted reviewers count |
| A Security finding's code-scanning alert dismissed in GitHub | an alert-dismissal event | As a dismissal |
| The PR merges with findings still posted and no signal | `ignored` | Against acceptance |

## Roles and weights

Every event carries the actor's role on the PR: `author`, `reviewer` (with participation before the event), `owner` (a member of the rule's owning team), or `observer`. `feedback.role_weights` in `global.yaml` weights them (the example: author 1, reviewer 2, owner 4, observer 0), `feedback.reaction_weight` sets the 👎 weight, and a login without a GitHub identity (`oidc:<username>`) is always an observer.

## The web UI

**Mark false positive** on a finding's page records `fb_wrong` from the signed-in viewer with their PR role. Owner actions (shadow, kill, kill switch, kill candidate) are not feedback events; they are config changes, listed at `/ui/actions` ([Web UI pages](web-ui.md)).
