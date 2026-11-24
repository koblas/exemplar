## 12. Observability and dashboards

### 12.1 Telemetry

- **Tracing:** OpenTelemetry Go SDK (`otelhttp` on every client and server, River middleware for jobs). One trace per review with spans per rule run, model call (tokens and cost attributes), tool call and GitHub API call. Python jobs emit the same.
- **Backend:** the organization's existing APM via the OTEL collector.
- **Transcripts:** every agent transcript to object storage, retained per `platform/retention.yaml` (default 90 days, §13.1).
- **Alerts:** share of runs hitting the soft ceiling > 5% in a day; share of runs incomplete > 2% in a day; zero-coverage > 2% of rule runs; webhook backlog > 5 min; any GitHub posting failure; acceptance drop > 15 points week-over-week for any rule; `exemplar-search` index lag > 1 hour.

### 12.2 Dashboard surfaces

Per-rule visibility and an auto-approve audit trail are required by the PRD's feedback and approval requirements [FR-B4, FR-A4]. Phases 2–5 use **Grafana** on the Postgres read replica (backed by SQL views) plus the OTEL backend, **River UI** for the job queue, and **GitHub-native** surfaces (check-run summaries, weekly owner digests). A **Go web UI** follows in Phase 6 for permission-aware code views, candidate triage and owner actions.

### 12.3 Audiences and the questions they need answered

| Audience | Questions | Cadence |
|---|---|---|
| **Rule owners** | Is my rule helping? Acceptance trend, recent findings with outcomes, why was it rejected, eval scores, is it about to be demoted? | Weekly |
| **Platform on-call** | Is the system healthy? Webhook backlog, review latency, failures, zero-coverage rate, model errors, rate-limit headroom | Real time |
| **Platform / FinOps** | What does it cost? Per review, per rule, per repo, cache-hit ratio, wasted tokens | Weekly |
| **Security / compliance** | What did the bot approve, and why? Every gate value, the categorization, post-merge reverts/incidents on auto-approved PRs | On demand + monthly |
| **Eng leadership** | Is it working? Acceptance vs. human baseline, LGTM recall, share of PRs merged AI-reviewed, time-to-merge | Monthly |
| **PR authors** | What did it check on my PR? | Per PR (GitHub-native) |

### 12.4 Staging

| Phase | Surface | Effort |
|---|---|---|
| 2–3 | **Grafana** with the Postgres read replica as a data source (SQL views below) and the OTEL backend for latency/traces; dashboards stored as JSON in `dashboards/` and provisioned, so they're reviewed like code | 3–5 days |
| 2–3 | **River UI** (ships with River) for queue depth, failed and retrying jobs | Hours |
| 3 | **GitHub-native:** check-run summary on every PR (rules run, coverage, cost); a **weekly owner digest** opened as an issue in `exemplar-config` (or posted to Slack) per owner team: their rules' acceptance, top rejected findings, demotion warnings | 2–3 days |
| 2 | **Lane/verifier and baseline boards** | 2 days |
| 4 | **Shift-left board** (exports, CLI pre-PR usage, upstream effectiveness) | 1 day |
| 5 | **Auto-approve audit and counter-metric boards** in Grafana + alerting rules | 2–3 days |
| 6 | **Custom Go web UI** served by `exemplar-api` (§12.8) | 3–5 weeks |

### 12.5 Grafana boards (v1)

- **Rule health:** table of all active/shadow rules with posted findings (7d/28d), acceptance, rejection, Wilson lower bound, trend sparkline, eval precision/recall, days since last change; red rows are within 5 points of the demotion threshold. Drill-down variable by rule shows recent findings with a link to the GitHub comment.
- **Adjudication:** owner queue size per team, age of oldest unadjudicated author flag, uphold/overturn ratio per rule.
- **Operations:** webhook-to-first-comment latency p50/p95; review duration by stage; River queue depth and job failures; zero-coverage, schema-error and timeout rates by rule; model 429/5xx rates; GitHub rate-limit remaining per installation.
  - **As implemented (OTel):**
    - Destinations come only from the standard `OTEL_*` environment variables, over OTLP/HTTP.
    - The instruments are `exemplar.model.calls` (by provider, model, stage, caller and outcome), `exemplar.model.duration`, `exemplar.review.duration` (by outcome), `exemplar.review.queue_to_post`, `exemplar.github.rate_limit.remaining` (by installation and resource) and `exemplar.github.requests`.
    - "Webhook to first comment" is measured from the review job's enqueue, which the router does right after receipt.
    - `dashboards/telemetry.json` reads them from a Prometheus-compatible datasource.
    - Per-stage engine timing is not split out yet. Review duration is whole-job.
- **Informational metrics** (PRD §13.2): cost per run and per PR lifetime by lane and stage; time to first comment and run duration; coverage per run and share of degraded or incomplete runs, with shed reasons; queue depth, wait time and runners in flight per repo; usage and cost by caller; failover rate per provider; **raw (unweighted) acceptance** beside the weighted goal metric; **review rounds to merge per PR** (mean, and the share needing five or more, from `reviews.round`) and how often the round cap engaged; **human approvals per PR** (median and p95) and **time waiting on a person** (from the first review submission to the merge, less time the PR waited on CI), paired with the counter-metrics. No targets are drawn on these panels.
- **Auto-approve audit:** approvals per day; table of every decision with each Stage 1 value against threshold (author merges per area, owner areas touched, candidates by severity, the `suggestion` findings posted with the approval), the Stage 2 categorization and reasons, links to PR and transcript key; revocations by reason, base drift included; reverts and incident-linked PRs among auto-approved merges, each with its `approval_failures` row and `should_have_caught`; kill-switch state per repo.
- **Program:** weekly **severity-weighted** acceptance (bot vs. human baseline, same definitions per §9.2), **incident-set recall** and the clean-PR false-alarm and harmful rates (§8.8), LGTM recall from the nightly golden-set run, share of merged PRs that were fully AI-reviewed, median time-to-merge for auto-approve-eligible vs. other PRs.
- **Lanes and verifier:** findings and acceptance by lane; verifier refutation rate per rule (a rule refuted > 50% of the time needs a better prompt); **quorum agreement per bug class** (share of candidates confirmed 3-of-3, 2-of-3, 1-of-3, 0) and the `quorum_shed` count; verifier calibration accuracy on known-wrong vs. known-true findings, with the pass/fail that gates `same_family`; held-back counts by `suppressed_reason`, importance priors in force and what they withheld; share of findings with a valid fix and suggestion-applied rate.
- **Baseline comparison:** Exemplar vs. the in-house baseline reviewer on the same PRs — precision against human comments, accepted-equivalent findings per PR, cost per accepted finding.
- **Shift left:** exports per repo, their PR state and config-SHA drift; CLI `exemplar review` calls by user and declared agent; per exported rule, violations per in-scope PR before vs. after export and agent-authored vs. other PRs.
- **Counter-metrics** (PRD §13.1): revert and change-failure rate of auto-approved vs. human-approved PRs; incidents linked to PRs with zero findings and quick approval; per team, share of owned paths changed with no team member viewing the PR; human substantive-comment volume over time.

**As implemented** (counter-metrics and the auto-approve audit, migration 00022):
- **Merges.** A merged PR's head and time go into `pr_merges` from the merge webhook.
- **Failures.** The path-events job records which PR a revert undid: GitHub's "Reverts owner/repo#N", or the PR of the commit a "This reverts commit" names. The incident feed's PR links go in as well. Both land in `pr_links`.
- **Outcomes.** `merge_outcomes` gives one row per merged PR: whether a production review of the merged head passed every gate, what Exemplar posted, and whether the PR was later reverted or named in an incident (change failure).
- **Boards.**
  - `counter-metrics.json` shows change failure of auto-approved against human-approved merges, weekly revert rates, incidents on quiet quick merges (nothing posted, and approved by the gates or merged within an hour of the first review), and every failed merge.
  - `auto-approve.json` adds failed auto-approved merges and each repo's kill switch, as its latest decision saw it.
- **Alerts** (`dashboards/provisioning/alerts.yaml`):
  - any failed auto-approved merge in 28 days;
  - auto-approved merges failing more often than human-approved ones, once there are 20;
  - any approval from a degraded or incomplete review.
- **Not built** (their inputs aren't recorded):
  - owned paths no team member viewed;
  - human substantive-comment volume;
  - the program board.

### 12.6 SQL views behind the boards

Keep dashboard queries on views so metric definitions live in one migration-controlled place, not scattered across panels.

```sql
-- Per-finding outcome, implementing the §9.1 precedence and §9.2 definitions.
-- The roll-ups below read from effective_events, a view that keeps only each actor's latest
-- explicit event per finding, drops role=observer, and ignores everything else once an
-- owner adjudication exists; so accepted and rejected are mutually exclusive per actor.
CREATE VIEW finding_outcomes AS
WITH adj AS (                              -- owner adjudication overrides other signals
  SELECT finding_id, bool_or(kind = 'adjudicated_uphold') AS upheld,
         bool_or(kind = 'adjudicated_overturn') AS overturned
  FROM feedback_events WHERE kind LIKE 'adjudicated_%' GROUP BY finding_id)
SELECT f.id, f.rule_id, f.lane, f.repo, f.created_at,
       CASE f.severity WHEN 'blocking' THEN 3 WHEN 'important' THEN 2 ELSE 1 END AS sev_w,
       (f.state IN ('posted','resolved','minimized'))                           AS seen,
       bool_or(e.kind IN ('applied_suggestion','fb_fixed','fixed'))             AS accepted,
       bool_or(e.kind IN ('fb_wrong','fb_not_important','fb_handled',
                          'fb_disagree','react_down'))                          AS rejected,   -- react_down: acceptance only, weight 0.5
       bool_or(e.kind = 'ignored')                                              AS ignored,
       bool_or(e.kind = 'fb_handled')                                           AS handled,
       bool_or(e.kind = 'human_agreed')                                         AS human_agreed,
       -- weighted eFP inputs (§9.2)
       CASE WHEN a.finding_id IS NOT NULL THEN CASE WHEN a.overturned THEN 4 ELSE 0 END
            ELSE coalesce(max(e.weight) FILTER (WHERE e.kind IN ('fb_wrong','fb_not_important','fb_disagree')), 0) END AS neg_w,  -- no react_down in eFP
       CASE WHEN a.finding_id IS NOT NULL THEN CASE WHEN a.upheld THEN 4 ELSE 0 END
            ELSE coalesce(max(e.weight) FILTER (WHERE e.kind IN ('applied_suggestion','fb_fixed','fixed','human_agreed')), 0) END AS pos_w,
       count(DISTINCT e.actor) FILTER (WHERE e.role <> 'author'
             AND e.kind IN ('fb_wrong','fb_not_important','fb_disagree','react_down')) AS non_author_negatives,
       bool_or(a.overturned)                                                    AS owner_overturned
FROM findings f
LEFT JOIN feedback_events e ON e.finding_id = f.id
LEFT JOIN adj a ON a.finding_id = f.id
WHERE f.state IN ('posted','resolved','minimized')
   OR f.suppressed_reason = 'human_duplicate'
GROUP BY f.id, f.severity, a.finding_id, a.upheld, a.overturned;

CREATE MATERIALIZED VIEW rule_weekly_stats AS
SELECT rule_id, lane,
       date_trunc('week', created_at)                                 AS week,
       count(*) FILTER (WHERE seen)                                   AS posted,
       count(*) FILTER (WHERE seen AND accepted)                      AS accepted,
       count(*) FILTER (WHERE seen AND rejected)                      AS rejected,
       count(*) FILTER (WHERE seen AND ignored)                       AS ignored,
       count(*) FILTER (WHERE handled)                                AS handled,
       count(*) FILTER (WHERE human_agreed)                           AS human_agreed,
       round(count(*) FILTER (WHERE seen AND accepted)::numeric
             / nullif(count(*) FILTER (WHERE seen AND (accepted OR rejected OR ignored)), 0), 3) AS acceptance_rate,
       -- severity-weighted acceptance (PRD G1, v0.9): blocking 3, important 2, suggestion 1
       round(sum(sev_w) FILTER (WHERE seen AND accepted)::numeric
             / nullif(sum(sev_w) FILTER (WHERE seen AND (accepted OR rejected OR ignored)), 0), 3) AS acceptance_weighted,
       round(sum(neg_w)::numeric / nullif(sum(neg_w + pos_w), 0), 3) AS effective_fp_weighted,
       sum(non_author_negatives)                                      AS non_author_negatives,
       count(*) FILTER (WHERE owner_overturned)                       AS owner_overturns
FROM finding_outcomes
GROUP BY 1, 2, 3;
CREATE UNIQUE INDEX ON rule_weekly_stats (rule_id, lane, week);  -- enables REFRESH ... CONCURRENTLY

CREATE MATERIALIZED VIEW review_daily_stats AS
SELECT date_trunc('day', r.started_at) AS day, r.repo, r.caller,
       count(*)                                                     AS reviews,
       percentile_cont(0.5)  WITHIN GROUP (ORDER BY r.cost_usd)     AS cost_p50,
       percentile_cont(0.95) WITHIN GROUP (ORDER BY r.cost_usd)     AS cost_p95,
       percentile_cont(0.95) WITHIN GROUP (ORDER BY extract(epoch FROM r.finished_at - r.started_at)) AS dur_p95_s,
       count(*) FILTER (WHERE r.status = 'incomplete')              AS incomplete,
       count(*) FILTER (WHERE r.verdict = 'APPROVE')                AS approvals,
       count(*) FILTER (WHERE r.degraded)                           AS degraded,
       avg(r.coverage)                                              AS coverage_avg
FROM reviews r WHERE r.status IN ('done','incomplete')
GROUP BY 1, 2, 3;
CREATE UNIQUE INDEX ON review_daily_stats (day, repo, caller);
```

**As implemented** (migration 00017), the views refine the sketch above:
- **`effective_events` is defined:**
  - observers are dropped;
  - each actor's latest signal on a finding wins, whether they gave it or it was inferred for them, such as an applied suggestion or a reviewer's agreement;
  - `resolved` and `ignored` are always kept;
  - once an owner adjudicates, only the latest adjudication counts.
- **`finding_outcomes` reads `effective_events`:**
  - **Which findings:** production reviews only. `seen` includes `outdated`, since such a finding was posted. `minimized` findings are late linter duplicates, which count against neither precision nor feedback (§6.10), so they are left out.
  - **Adjudication:** an adjudicated finding is accepted on `uphold` and rejected on `overturn`. Its eFP weight is the adjudication's own (the owner weight).
  - **Casts:** boolean and weight columns are cast explicitly, and the eFP ratio is computed in `numeric`.

A River periodic job refreshes the materialized views every 15 minutes (`REFRESH MATERIALIZED VIEW CONCURRENTLY`). The same views feed the weekly owner digest and the auto-demotion job, so the dashboard, the digest and the automation can never disagree about a rule's acceptance.

### 12.7 Access control

Findings, explanations and especially transcripts contain source code. Grafana's permissions are per dashboard or folder, not per GitHub repository, so it cannot easily show each viewer only the repos they can read. The rule for v1:

- **Grafana shows aggregates, rule ids, repo names and links only** — no code, no finding text, no transcripts. Clicking through lands on GitHub, which enforces repo permissions.
- **Transcripts stay in object storage**, reachable only through `exemplar-api`, which checks the viewer's access to that repo with their GitHub user token (GitHub App user-to-server OAuth) before returning a short-lived signed URL. Until Phase 6, platform on-call retrieves transcripts through the CLI (`exemplar transcript <review-id>`), which uses the same check.

### 12.8 Phase 6 web UI (Go)

Scope: permission-aware code views, candidate triage at volume, and one-click owner actions.

| Page | Contents | Actions |
|---|---|---|
| Rules | All rules with health metrics, filters by owner/category/status | — |
| Rule detail | Acceptance trend, recent findings with outcome and author replies, eval results, source comments it was mined from | Shadow, kill, edit (each opens a config PR — git stays the source of truth) |
| Finding detail | Diff context, explanation, feedback timeline, transcript viewer | Mark false positive (writes a feedback event + eval-case suggestion) |
| Candidates | Ranked queue with cluster members and citations | Open config PR, kill with reason |
| Playground | Paste or pick a PR, edit a draft rule, run, see findings and cost | Save as candidate PR |
| Auto-approve audit | Decision list, gate traces, transcript links | Toggle a repo's kill switch (opens and auto-merges the config PR, with the actor recorded) |
| Cost explorer | Spend by any dimension, outliers | — |

**Stack:** server-rendered **`templ` + htmx** inside `exemplar-api`. **Auth:** org SSO (OIDC) for identity plus GitHub user OAuth for repo-level authorization checks, cached for 5 minutes.

**As implemented (sign-in sources).** exemplar-api follows Coder's model (coder.com/docs/admin/users/oidc-auth and github-auth): GitHub OAuth and OpenID Connect are alternative sign-in sources. Either or both can be on, and `/login` offers whichever are configured.
- **GitHub OAuth** (`EXEMPLAR_OAUTH2_GITHUB_*`): client id and secret, `ALLOWED_ORGS` or `ALLOW_EVERYONE` (one is required), and `ENTERPRISE_BASE_URL`.
  - Membership is checked with the person's own token at sign-in and on `/v1/evaluate`.
  - Repo access is their GitHub permission, cached for 5 minutes.
- **OpenID Connect** (`EXEMPLAR_OIDC_*`, `internal/oidc`):
  - Settings: issuer (discovered at startup), client, `EMAIL_DOMAIN`, `SCOPES`, `EMAIL_FIELD`, `USERNAME_FIELD`, `GROUP_FIELD`, `ALLOWED_GROUPS`, `IGNORE_EMAIL_VERIFIED`, `IGNORE_USERINFO`, `AUTH_URL_PARAMS`, `SIGN_IN_TEXT` and `ICON_URL`.
  - The code flow uses state, nonce and PKCE (S256). The ID token is verified against the provider's JWKS and merged with userinfo, which must have the same subject.
  - **OIDC replaces GitHub for authorization:** repo access comes only from the person's groups, through `EXEMPLAR_OIDC_GROUP_REPOS` (`{"group": ["owner/name", "owner/*", "*"]}`). GitHub permissions are not consulted.
  - **Connect GitHub.** With both sources configured, an OIDC sign-in is followed by connecting the person's GitHub account, once.
    - It uses GitHub OAuth with the same client and the allowed-orgs check.
    - The link is stored in `identity_links` (migration 00027) by issuer and subject. A GitHub account can belong to only one OIDC identity.
    - From then on the person *is* that GitHub login, on the web and for `/v1/evaluate`: feedback roles, rule-owner teams, owner actions, playground and budgets. Repo access stays with their groups.
    - `EXEMPLAR_OIDC_GITHUB_LINK` is `required` (the default), `optional` (offers "continue without", as the OIDC username) or `off`. With `off`, or for those who skip, the username stands in for a GitHub login, so map `USERNAME_FIELD` to a GitHub-username claim if one exists.
    - `EXEMPLAR_OAUTH2_GITHUB_SIGN_IN=false` keeps GitHub for connecting only.
    - `/ui/connect-github` connects a different account.
- **The CLI.** `exemplar login --oidc` uses the device flow against the provider's CLI client (`EXEMPLAR_OIDC_CLI_CLIENT_ID`), and refreshes the ID token with `offline_access`. `/v1/evaluate` verifies those ID tokens with the same group mapping, recorded as `caller=cli`.
- **GitHub off.** With GitHub sign-in off, GitHub user tokens are refused everywhere.
- **Sessions.** The session cookie holds the username. The `/ui/`-scoped sealed cookie holds the GitHub token or the OIDC repo patterns.

**As implemented** (first slice, `internal/web`, served at `/ui/` by exemplar-api):
- **Pages:**
  - Rules: filters by owner, status and search; 28-day posted counts, acceptance and weighted eFP.
  - Rule detail: weekly outcomes and recent findings with their outcome.
  - Finding detail: explanation, suggested fix, verifier verdict and the feedback timeline.
  - Candidates: open candidates by score.
  - Auto-approve audit: the latest 500 decisions with Stage 1 reasons and Stage 2.
  - Cost: spend by caller, and by day, caller, stage and provider.
- **Stack.** `html/template`, server-rendered, with no JavaScript and a CSP of `default-src 'none'`. These read-only pages need neither templ's code generator nor htmx, so both are left out.
- **Auth:**
  - Sign-in is the feedback links' GitHub OAuth, with org SSO at the ingress.
  - A sign-in that returns to `/ui/` also keeps the user token, sealed with AES-GCM under a key derived from the session key. It sits in an HttpOnly cookie sent only to `/ui/`, and is never stored server-side or logged.
  - Every repo-specific row is shown only if the token's user can read that repo: findings, PRs, decisions, repo-scoped rules, and candidates (whose text is drawn from their source comments). Checks go through `internal/ghuser`, cached by token hash for 5 minutes and shared with `/v1/evaluate`.
  - A finding in an unreadable repo gets the same 404 as a missing one.
  - When an access check fails, those rows are hidden and the page says so.
  - Aggregates without repos (rule health, spend) are shown to every signed-in viewer.
- **Owner actions** go through a worker job, so exemplar-api still holds no installation credentials (`internal/owneract`, migration 00024).
  - A form, signed for the viewer, the action and its target, records the action with its actor in `owner_actions` and enqueues `owner_action`.
  - exemplar-worker checks the actor with the gate token, then writes exemplar-config with the config-writer token, both scoped to the config repo. It opens a PR naming the actor and the reason, and merges it where branch protection allows:
    - move a global rule to shadow or kill it: its owning team or the platform team;
    - kill a candidate, with a required reason: its owner or the platform team. This is a database update.
    - turn a repo's auto-approve off: the platform team or the repo's `approvable_owners`. Turning it on stays a reviewed config PR.
  - "Mark false positive" enqueues the same `feedback_link` job a feedback link does, so the worker derives the role.
  - `/ui/actions` shows the viewer's actions and their outcome.
  - The platform team is the worker's `--platform-team` (default `@platform/devex`).
- **Playground** (`/ui/playground`, migration 00026):
  - Paste a rule file, or start from an existing rule (its latest version, rendered to YAML), and name a PR you can read.
  - exemplar-api checks the rule's schema, the viewer's read access with their token, and a daily per-viewer budget (`--playground-daily-usd`, `--playground-daily-runs`).
  - It records a `playground_runs` row and enqueues `playground_run`.
  - exemplar-worker loads the deployed config with the draft added and runs the rule alone on the PR (`playground.Runner`, `caller=playground`, the same path as `exemplar try`). Nothing is posted.
  - The run page shows the findings that would post and those dropped (with why), plus the cost. It offers "edit and run again".
  - **Save as candidate PR** is the owner action `propose_rule`, available only to the person who ran the try. A new rule id becomes `rules/<category>/<id>.yaml` with `status: candidate`. An existing global rule's file is replaced, for its owners or the platform team. Either way it opens a draft config PR that is never merged by Exemplar. This is also how a rule is edited from the UI.
  - Runs follow the findings retention period.
- **Not built:**
  - an eval-case suggestion from "mark false positive";
  - diff context and the transcript viewer (transcripts are not stored yet);
  - eval results and source comments on rule detail.

---
