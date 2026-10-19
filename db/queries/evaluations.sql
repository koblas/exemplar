-- name: CreateEvaluation :one
INSERT INTO evaluations (repo, base_sha, requested_by, caller, agent, diff, mode, head_sha, rules, requester_scopes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id;

-- name: GetEvaluation :one
SELECT id, repo, base_sha, requested_by, caller, agent, status, reason, review_id, result, cost_usd, created_at, finished_at, mode
FROM evaluations WHERE id = $1;

-- name: StartEvaluation :one
-- Claims a queued evaluation and returns its diff.
UPDATE evaluations SET status = 'running' WHERE id = $1 AND status IN ('queued', 'running')
RETURNING repo, base_sha, requested_by, caller, agent, coalesce(diff, '')::text AS diff, mode, coalesce(head_sha, '')::text AS head_sha, rules, requester_scopes;

-- name: FinishEvaluation :exec
UPDATE evaluations SET status = $2, reason = $3, review_id = $4, result = $5, cost_usd = $6, diff = NULL, finished_at = now()
WHERE id = $1;

-- name: EvaluationSpend :one
-- What a requester's evaluations cost since @since.
SELECT coalesce(sum(cost_usd), 0)::float8 AS spent, count(*)::int AS runs
FROM evaluations WHERE requested_by = @requested_by AND created_at >= @since;

-- name: RepoInstallationID :one
-- The installation of a repo, from its latest webhook delivery.
SELECT installation_id FROM gh_events WHERE repo_full_name = $1 AND installation_id IS NOT NULL
ORDER BY received_at DESC LIMIT 1;
