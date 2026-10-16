-- name: GetExport :many
SELECT * FROM exports WHERE repo = $1 AND path = $2;

-- name: UpsertExport :exec
INSERT INTO exports (repo, path, target, content_hash, base_hash, config_sha, pr_url, state, auto_merged)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (repo, path) DO UPDATE SET target = excluded.target, content_hash = excluded.content_hash,
  base_hash = excluded.base_hash, config_sha = excluded.config_sha, pr_url = excluded.pr_url,
  state = excluded.state, auto_merged = excluded.auto_merged, updated_at = now();

-- name: SetExportState :exec
UPDATE exports SET state = $3, updated_at = now() WHERE repo = $1 AND path = $2;

-- name: RecordExportRun :exec
INSERT INTO export_runs (config_sha) VALUES ($1) ON CONFLICT (config_sha) DO NOTHING;

-- name: MarkExportCurrent :exec
UPDATE exports SET state = 'current', current_sha = $3, current_at = now(), landed_at = coalesce(landed_at, now()), updated_at = now()
WHERE repo = $1 AND path = $2;

-- name: ExportDrift :many
SELECT repo, versions_behind, files, proposed, edited, coalesce(pr_url, '')::text AS pr_url
FROM export_drift WHERE versions_behind >= $1 ORDER BY versions_behind DESC, repo;

-- name: UpstreamRuleStats :many
-- Per exported rule over the last @days days: violations and in-scope
-- PRs whose agent received the export, the rest, and in exporting repos
-- before and after the export landed.
SELECT rule_id,
  count(*) FILTER (WHERE received)::int AS received_prs,
  coalesce(sum(violations) FILTER (WHERE received), 0)::int AS received_violations,
  count(*) FILTER (WHERE NOT received)::int AS other_prs,
  coalesce(sum(violations) FILTER (WHERE NOT received), 0)::int AS other_violations,
  count(*) FILTER (WHERE export_landed > first_review)::int AS before_prs,
  coalesce(sum(violations) FILTER (WHERE export_landed > first_review), 0)::int AS before_violations,
  count(*) FILTER (WHERE export_landed <= first_review)::int AS after_prs,
  coalesce(sum(violations) FILTER (WHERE export_landed <= first_review), 0)::int AS after_violations
FROM upstream_pr_rule
WHERE first_review > now() - make_interval(days => @days::int)
GROUP BY rule_id ORDER BY rule_id;

-- name: SetReviewAgent :exec
UPDATE reviews SET agent = $2, agent_reads = $3 WHERE id = $1;
