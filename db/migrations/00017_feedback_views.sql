-- Feedback roll-ups: signal precedence, eFP definitions and dashboard views.
-- The dashboards, the owner digest and auto-demotion all read these, so
-- they can never disagree about a rule's acceptance.

-- +goose Up

-- +goose StatementBegin
-- effective_events applies signal precedence:
--   * observers (weight 0, audit only) are dropped;
--   * each actor's latest signal on a finding supersedes their earlier ones
--     (Wrong, then Fixed, is Fixed), whether they gave it or it was
--     inferred for them (an applied suggestion, a reviewer's agreement);
--   * thread resolution and "ignored" are not an actor's judgment and
--     are always kept;
--   * once an owner adjudicates, only the latest adjudication counts.
CREATE VIEW effective_events AS
WITH e AS (
  SELECT id, finding_id, kind, actor, role, weight, via, observed_at
  FROM feedback_events WHERE role <> 'observer'
), adj AS (
  SELECT DISTINCT ON (finding_id) * FROM e WHERE kind LIKE 'adjudicated\_%'
  ORDER BY finding_id, observed_at DESC, id DESC
), latest AS (
  SELECT DISTINCT ON (finding_id, actor) * FROM e
  WHERE kind NOT LIKE 'adjudicated\_%' AND kind NOT IN ('resolved', 'ignored')
  ORDER BY finding_id, actor, observed_at DESC, id DESC
)
SELECT * FROM adj
UNION ALL
SELECT * FROM latest WHERE NOT EXISTS (SELECT 1 FROM adj WHERE adj.finding_id = latest.finding_id)
UNION ALL
SELECT * FROM e WHERE kind IN ('resolved', 'ignored')
  AND NOT EXISTS (SELECT 1 FROM adj WHERE adj.finding_id = e.finding_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- finding_outcomes is one row per production finding someone saw (posted,
-- later resolved or outdated) or that a reviewer had already raised
-- (human_duplicate). Late linter duplicates (minimized) count
-- against neither precision nor feedback and are left out.
-- An adjudication decides the finding: uphold accepts it and is the eFP
-- positive, overturn rejects it and is the negative, each at its weight.
CREATE VIEW finding_outcomes AS
SELECT f.id, f.rule_id, f.lane, f.repo, f.created_at,
       (f.state IN ('posted', 'resolved', 'outdated'))::boolean                                AS seen,
       (coalesce(bool_or(e.kind = 'adjudicated_uphold'), false)
         OR (NOT coalesce(bool_or(e.kind LIKE 'adjudicated\_%'), false)
             AND coalesce(bool_or(e.kind IN ('applied_suggestion', 'fb_fixed', 'fixed')), false)))::boolean AS accepted,
       (coalesce(bool_or(e.kind = 'adjudicated_overturn'), false)
         OR (NOT coalesce(bool_or(e.kind LIKE 'adjudicated\_%'), false)
             AND coalesce(bool_or(e.kind IN ('fb_wrong', 'fb_not_important', 'fb_handled', 'fb_disagree', 'react_down')), false)))::boolean
                                                                                                AS rejected,
       coalesce(bool_or(e.kind = 'ignored'), false)::boolean                                   AS ignored,
       coalesce(bool_or(e.kind = 'fb_handled'), false)::boolean                                AS handled,
       coalesce(bool_or(e.kind = 'human_agreed'), false)::boolean                              AS human_agreed,
       -- eFP inputs: reactions are outside eFP.
       coalesce(max(e.weight) FILTER (WHERE e.kind IN ('fb_wrong', 'fb_not_important', 'fb_disagree', 'adjudicated_overturn')), 0)::real AS neg_w,
       coalesce(max(e.weight) FILTER (WHERE e.kind IN ('applied_suggestion', 'fb_fixed', 'fixed', 'human_agreed', 'adjudicated_uphold')), 0)::real AS pos_w,
       count(DISTINCT e.actor) FILTER (WHERE e.role <> 'author'
             AND e.kind IN ('fb_wrong', 'fb_not_important', 'fb_disagree', 'react_down'))        AS non_author_negatives,
       coalesce(bool_or(e.kind = 'adjudicated_overturn'), false)::boolean                      AS owner_overturned
FROM findings f
JOIN rule_runs rr ON rr.id = f.rule_run_id
JOIN reviews r ON r.id = rr.review_id AND r.caller = 'production'
LEFT JOIN effective_events e ON e.finding_id = f.id
WHERE f.state IN ('posted', 'resolved', 'outdated') OR f.suppressed_reason = 'human_duplicate'
GROUP BY f.id;
-- +goose StatementEnd

-- +goose StatementBegin
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
       round(sum(neg_w)::numeric / nullif(sum(neg_w + pos_w), 0)::numeric, 3) AS effective_fp_weighted,
       sum(non_author_negatives)                                      AS non_author_negatives,
       count(*) FILTER (WHERE owner_overturned)                       AS owner_overturns
FROM finding_outcomes
GROUP BY 1, 2, 3;
-- +goose StatementEnd

CREATE UNIQUE INDEX rule_weekly_stats_key ON rule_weekly_stats (rule_id, lane, week);

-- +goose StatementBegin
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
FROM reviews r WHERE r.status IN ('done', 'incomplete') AND r.started_at IS NOT NULL
GROUP BY 1, 2, 3;
-- +goose StatementEnd

CREATE UNIQUE INDEX review_daily_stats_key ON review_daily_stats (day, repo, caller);

-- +goose Down
DROP MATERIALIZED VIEW review_daily_stats;
DROP MATERIALIZED VIEW rule_weekly_stats;
DROP VIEW finding_outcomes;
DROP VIEW effective_events;
