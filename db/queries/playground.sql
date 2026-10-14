-- name: CreatePlaygroundRun :one
INSERT INTO playground_runs (actor, repo, pr_number, rule_id, rule_yaml) VALUES ($1, $2, $3, $4, $5) RETURNING id;

-- name: GetPlaygroundRun :one
SELECT * FROM playground_runs WHERE id = $1;

-- name: FinishPlaygroundRun :exec
UPDATE playground_runs SET status = $2, reason = $3, result = $4, cost_usd = $5, finished_at = now() WHERE id = $1 AND status = 'queued';

-- name: PlaygroundRunsBy :many
SELECT id, repo, pr_number, rule_id, status, cost_usd, created_at FROM playground_runs WHERE actor = $1 ORDER BY id DESC LIMIT 20;

-- name: PlaygroundSpend :one
SELECT coalesce(sum(cost_usd), 0)::float8 AS spent, count(*)::int AS runs
FROM playground_runs WHERE actor = @actor AND created_at >= @since;

-- name: UIRuleSpec :one
-- A rule's latest version (by its latest run), for the playground to start from.
SELECT ru.scope, ru.spec FROM rules ru
LEFT JOIN LATERAL (SELECT max(rr.id) AS last_run FROM rule_runs rr WHERE rr.rule_id = ru.rule_id AND rr.rule_version = ru.version_sha) l ON true
WHERE ru.rule_id = $1 ORDER BY l.last_run DESC NULLS LAST LIMIT 1;
