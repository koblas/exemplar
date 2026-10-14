-- name: CreateOwnerAction :one
INSERT INTO owner_actions (action, target, actor, reason) VALUES ($1, $2, $3, $4) RETURNING id;

-- name: GetOwnerAction :one
SELECT * FROM owner_actions WHERE id = $1;

-- name: FinishOwnerAction :exec
UPDATE owner_actions SET status = $2, result = $3, pr_url = $4, finished_at = now() WHERE id = $1 AND status = 'queued';

-- name: OwnerActionsBy :many
SELECT * FROM owner_actions WHERE actor = $1 ORDER BY id DESC LIMIT 50;

-- name: GetCandidateOwner :one
SELECT owner, status FROM candidates WHERE id = $1;

-- name: KillCandidate :execrows
UPDATE candidates SET status = 'killed', kill_reason = $2, updated_at = now() WHERE id = $1 AND status IN ('new', 'pr_open');
