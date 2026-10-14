-- name: CreateReview :one
-- Idempotent on the natural key: a retried job gets the same review back.
INSERT INTO reviews (repo, pr_number, head_sha, base_sha, prev_review_id, config_version, search_snapshot, caller, status, attempt)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'queued', $9)
ON CONFLICT (repo, pr_number, head_sha, config_version, caller, attempt)
DO UPDATE SET repo = EXCLUDED.repo
RETURNING *;

-- name: GetReview :one
SELECT * FROM reviews WHERE id = $1;

-- name: StartReview :exec
UPDATE reviews SET status = 'running', started_at = coalesce(started_at, now())
WHERE id = $1 AND status IN ('queued', 'running');

-- name: FinishReview :exec
UPDATE reviews
SET status = $2, coverage = $3, degraded = $4, shed = $5, incomplete_reason = $6,
    verdict = $7, bug_lane = $8, cost_usd = $9, finished_at = now()
WHERE id = $1;

-- name: UpsertApprovalDecision :exec
INSERT INTO approval_decisions (review_id, stage1, stage2, decision)
VALUES ($1, $2, $3, $4)
ON CONFLICT (review_id) DO UPDATE SET stage1 = EXCLUDED.stage1, stage2 = EXCLUDED.stage2, decision = EXCLUDED.decision;

-- name: HeadAutoApproved :one
-- Whether a production review of this head passed every auto-approve gate.
SELECT EXISTS (
  SELECT 1 FROM approval_decisions d JOIN reviews r ON r.id = d.review_id
  WHERE r.repo = $1 AND r.pr_number = $2 AND r.head_sha = $3 AND r.caller = 'production' AND d.decision = 'approved'
)::bool AS approved;

-- name: GetApprovalDecision :one
SELECT * FROM approval_decisions WHERE review_id = $1;

-- name: UpsertRule :exec
INSERT INTO rules (rule_id, version_sha, scope, kind, category, severity, status, owner, fix_policy, review_by, exported, model_tier, spec)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (rule_id, version_sha) DO NOTHING;

-- name: InsertGHEvent :execrows
INSERT INTO gh_events (delivery_id, event, action, installation_id, repo_full_name, payload)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (delivery_id) DO NOTHING;

-- name: GetGHEvent :one
SELECT * FROM gh_events WHERE delivery_id = $1;

-- name: MarkGHEventProcessed :exec
UPDATE gh_events SET processed_at = now() WHERE delivery_id = $1;

-- name: LatestReviewAttempt :many
-- A head's latest production review attempt and its status, so a Re-run
-- enqueues the next attempt of an incomplete review (spec §6.15). No row:
-- the head was never reviewed.
SELECT attempt, status FROM reviews
WHERE repo = $1 AND pr_number = $2 AND head_sha = $3 AND caller = 'production'
ORDER BY attempt DESC LIMIT 1;

-- name: StoredDeliveries :many
-- Which of these deliveries the receiver stored (the reconciler skips them).
SELECT delivery_id FROM gh_events WHERE delivery_id = ANY(@ids::uuid[]);

-- name: ReviewedHead :one
-- Whether a head has a production review, of any attempt (the sweeper
-- enqueues heads that do not).
SELECT EXISTS (
  SELECT 1 FROM reviews WHERE repo = $1 AND pr_number = $2 AND head_sha = $3 AND caller = 'production'
) AS reviewed;

-- name: MarkReviewSuperseded :exec
-- A newer push replaced this review's head (spec §6.2).
UPDATE reviews SET status = 'superseded', finished_at = coalesce(finished_at, now()) WHERE id = $1;
