# Web UI pages

exemplar-api serves the web UI under `/ui/`. Every page needs a sign-in (GitHub OAuth or OIDC; [How to set up sign-in](../guides/install/sign-in.md)); org SSO sits in front at the edge. Anything tied to a repository (findings, PRs, decisions, the comments a candidate was mined from, repo-scoped rules) is shown only for repositories the viewer can read: their GitHub permission (checked with their user token, cached for 5 minutes) when they signed in with GitHub, or the repositories their groups map to when they signed in with OIDC. Aggregates without repositories (rule health, spend) are shown to every signed-in viewer.

## Pages

| Path | Page | Shows |
|---|---|---|
| `/ui/` | Rules | Every rule the viewer may see, with its owner, status, scope and health over the window (posted, accepted, rejected, acceptance, eFP). Filters: `?owner=`, `?status=`, `?q=` (search). |
| `/ui/rules/{id}` | Rule | The rule's weekly acceptance and eFP, its last 100 findings with their state, links to try it in the playground, and the owner actions **Shadow** and **Kill** (owning team or platform team). |
| `/ui/findings/{id}` | Finding | One finding: repo and PR, lane, severity, anchor, message, suggested fix and whether it validated, the verifier's verdict, state and suppression reason, a link to the GitHub comment, and every feedback event with actor, role and kind. **Mark false positive** records feedback from the page. |
| `/ui/candidates` | Candidates | Mined candidates with their score, owner and the repositories they came from (source comments shown only for repos the viewer can read), and **Kill** with a required reason (owner or platform team). |
| `/ui/playground` | Playground | Paste a draft rule (or start from an existing one with `?rule=<id>`), pick a PR the viewer can read, and run the rule alone on it in exemplar-worker; nothing is posted. `/ui/playground/{id}` is a run's result; `?from=<id>` starts from an earlier run. **Save as draft PR** opens a config PR (a new rule: anyone; an edited global rule: its owners or the platform team). |
| `/ui/audit` | Auto-approve audit | The last 500 decisions the viewer may see, each with Stage 1 reasons, merge safety and Stage 2, linked to the PR, and each repository's kill switch (platform team or the repo's `approvable_owners`). |
| `/ui/cost` | Cost | Model spend and calls by caller over `?days=` (30 by default, 1 to 365), with totals. Visible to every signed-in viewer. |
| `/ui/actions` | Your actions | The owner actions the viewer requested, with their outcome. |
| `/ui/setup` | Setup checks | The installation's GitHub side: the App's installation, access to `exemplar-config`, GitHub's delivery log and the events stored, with what to fix. `/ui/setup.json` is the same as JSON. |
| `/ui/connect-github` | Connect GitHub | Where an OIDC user connects or switches their GitHub account. |
| `/ui/signout` | Sign out | `POST` ends the session. |

Owner actions (shadow or kill a rule, kill a candidate, a repo's kill switch, save a playground run as a draft PR) are recorded with the signed-in actor and run as a worker job, which checks the actor may take the action, then edits `exemplar-config` through a PR and merges it where branch protection allows, or updates the database for a candidate. Draft PRs are never merged by the job.

## Sign-in routes

| Route | Purpose |
|---|---|
| `GET /login` | The sign-in page with the configured sources; `?return=` sends the person back afterwards. |
| `GET /oauth/start`, `GET /oauth/callback` | GitHub OAuth. Register `https://<api>/oauth/callback` on the App (the setup page does). |
| `GET /oidc/start`, `GET /oidc/callback` | OpenID Connect. Register `https://<api>/oidc/callback` on the client. |
| `GET /connect/github`, `GET /connect/skip` | Connect a GitHub account after OIDC sign-in, or skip when `EXEMPLAR_OIDC_GITHUB_LINK` allows it. |
| `POST /ui/signout` | End the session. Sessions last 8 hours. |

## Feedback links

`GET /f/{finding}/{reason}?s=<signature>` is the link in a comment's footer, one per reason (`fixed`, `wrong`, `not_important`, `handled`, `disagree`). The signature is an HMAC over the finding and the reason with the link key, so a link can't be altered to another finding or reason. The page confirms with the signed-in GitHub identity and `POST` records the feedback; the role comes from the person's participation in the PR before the click.

## `/v1/evaluate`

The API behind `exemplar review`, config CI's `eval --remote`, and coding agents.

| Method | Path | Does |
|---|---|---|
| `POST` | `/v1/evaluate` | Submits a working-tree diff against a pushed base (or, for mode `eval`, a config PR's merge-base and head) and answers `202` with its id. |
| `GET` | `/v1/evaluate/{id}` | Its status and, when done, what would have been posted. Only the requester sees a result. |

Callers send either a GitHub user token (the CLI signs in with the review app's device flow) or, for services, a JWT from a configured source: an Auth0 access token for this API (`svc:<sub>`), or a GitHub Actions OIDC token (`gha:<owner>/<repo>`). A user must be able to read the repo; a service needs an `evaluate:<owner>/<repo>` (or `<owner>/*`, `*`) permission (an Actions token has one, for its own repo), and is recorded as `caller=agent`. Each caller is rate-limited and held to a daily budget. Tokens are never stored.

## Probes

`GET /healthz` and `GET /readyz` on every service's `http` port. Readiness turns false during shutdown.
