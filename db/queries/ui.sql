-- The web UI. Repo-specific rows carry their repo so
-- the handler can show only those the viewer can read.

-- name: UIRules :many
-- Each rule's latest version (by its latest run) with @days of outcomes.
WITH latest AS (
  SELECT DISTINCT ON (ru.rule_id) ru.rule_id, ru.scope, ru.kind, ru.category, ru.severity, ru.status, ru.owner, ru.exported
  FROM rules ru
  LEFT JOIN LATERAL (SELECT max(rr.id) AS last_run FROM rule_runs rr WHERE rr.rule_id = ru.rule_id AND rr.rule_version = ru.version_sha) l ON true
  ORDER BY ru.rule_id, l.last_run DESC NULLS LAST
), stats AS (
  SELECT rule_id, count(*) FILTER (WHERE seen)::int AS posted, count(*) FILTER (WHERE seen AND accepted)::int AS accepted,
    count(*) FILTER (WHERE seen AND (accepted OR rejected OR ignored))::int AS judged,
    coalesce(sum(neg_w), 0)::float8 AS neg_w, coalesce(sum(pos_w), 0)::float8 AS pos_w
  FROM finding_outcomes WHERE created_at > now() - make_interval(days => @days::int) GROUP BY rule_id
)
SELECT l.rule_id, l.scope, l.kind, l.category, l.severity, l.status, l.owner, l.exported,
  coalesce(s.posted, 0)::int AS posted, coalesce(s.accepted, 0)::int AS accepted, coalesce(s.judged, 0)::int AS judged,
  coalesce(s.neg_w, 0)::float8 AS neg_w, coalesce(s.pos_w, 0)::float8 AS pos_w
FROM latest l LEFT JOIN stats s ON s.rule_id = l.rule_id
ORDER BY l.rule_id;

-- name: UIRuleWeeks :many
SELECT week::timestamptz AS week, sum(posted)::int AS posted, sum(accepted)::int AS accepted, sum(rejected)::int AS rejected, sum(ignored)::int AS ignored,
  coalesce(round(sum(accepted)::numeric / nullif(sum(accepted + rejected + ignored), 0), 3), 0)::float8 AS acceptance
FROM rule_weekly_stats WHERE rule_id = $1
GROUP BY week ORDER BY week DESC LIMIT 12;

-- name: UIRuleFindings :many
SELECT f.id, f.repo, f.pr_number, f.path, f.start_line, f.end_line, f.state, f.created_at,
  coalesce(o.accepted, false)::bool AS accepted, coalesce(o.rejected, false)::bool AS rejected, coalesce(o.ignored, false)::bool AS ignored
FROM findings f LEFT JOIN finding_outcomes o ON o.id = f.id
WHERE f.rule_id = $1 AND f.state IN ('posted', 'resolved', 'outdated', 'minimized')
ORDER BY f.id DESC LIMIT $2;

-- name: UIFinding :one
SELECT f.id, f.rule_id, f.repo, f.pr_number, f.lane, f.severity, f.path, f.side, f.start_line, f.end_line, f.message,
  coalesce(f.suggested_fix, '')::text AS suggested_fix, coalesce(f.fix_valid, false)::bool AS fix_valid,
  coalesce(f.verify_verdict, '')::text AS verify_verdict, f.state, coalesce(f.suppressed_reason, '')::text AS suppressed_reason,
  coalesce(f.gh_comment_id, 0)::bigint AS gh_comment_id, f.created_at
FROM findings f WHERE f.id = $1;

-- name: UIFindingEvents :many
SELECT kind, actor, role, weight, via, coalesce(reason_text, '')::text AS reason_text, observed_at
FROM feedback_events WHERE finding_id = $1 ORDER BY observed_at, id;

-- name: UICandidates :many
-- Open candidates by score, with the repos their source comments came from.
SELECT c.id, c.title, c.description, c.cluster_size, c.distinct_reviewers, c.score, c.owner, c.status,
  coalesce(c.config_pr_url, '')::text AS config_pr_url,
  coalesce((SELECT array_agg(DISTINCT m.repo) FROM candidate_sources s JOIN mined_comments m ON m.id = s.comment_id WHERE s.candidate_id = c.id), '{}')::text[] AS repos
FROM candidates c WHERE c.status IN ('new', 'pr_open')
ORDER BY c.score DESC LIMIT $1;

-- name: UIDecisions :many
SELECT r.id AS review_id, coalesce(r.finished_at, r.started_at) AS at, r.repo, r.pr_number, r.head_sha, d.decision,
  coalesce((SELECT string_agg(x, ', ') FROM jsonb_array_elements_text(coalesce(d.stage1->'reasons', '[]'::jsonb)) x), '')::text AS stage1_reasons,
  coalesce(d.stage2->>'decision', '')::text AS stage2
FROM approval_decisions d JOIN reviews r ON r.id = d.review_id
WHERE r.caller = 'production'
ORDER BY r.id DESC LIMIT $1;

-- name: UICost :many
-- Model spend by day, caller, stage and provider over @days days.
SELECT date_trunc('day', at)::timestamptz AS day, coalesce(caller, '')::text AS caller, stage, provider,
  count(*)::int AS calls, coalesce(sum(cost_usd), 0)::float8 AS cost_usd
FROM model_calls WHERE at > now() - make_interval(days => @days::int)
GROUP BY 1, 2, 3, 4 ORDER BY 1 DESC, 6 DESC;
