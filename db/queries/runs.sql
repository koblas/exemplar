-- name: UpsertRuleRun :one
-- Idempotent: a retried review reuses the rule run, and its status says
-- whether the work is already done.
INSERT INTO rule_runs (review_id, rule_id, rule_version, lane, scoped_files, status)
VALUES ($1, $2, $3, $4, $5, 'pending')
ON CONFLICT (review_id, rule_id, rule_version) DO UPDATE SET lane = EXCLUDED.lane
RETURNING *;

-- name: FinishRuleRun :exec
UPDATE rule_runs
SET status = $2, coverage = $3, context_degraded = $4, model = $5, input_tokens = $6,
    cached_tokens = $7, output_tokens = $8, cost_usd = $9, latency_ms = $10, transcript_key = $11,
    carried_from = $12
WHERE id = $1;

-- name: ListRuleRuns :many
SELECT * FROM rule_runs WHERE review_id = $1 ORDER BY id;

-- name: InsertFinding :one
INSERT INTO findings (rule_run_id, repo, pr_number, rule_id, lane, severity, cc_label, path, side,
                      start_line, end_line, fingerprint, message, verify_verdict, verify_importance,
                      verify_model, verify_same_family, verify_evidence, suggested_fix, fix_valid,
                      rank_score, state, suppressed_reason, anchor_code, anchor_ordinal)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)
ON CONFLICT (rule_run_id, fingerprint) DO UPDATE SET state = findings.state
RETURNING id;

-- name: ListFindingsByReview :many
SELECT f.* FROM findings f JOIN rule_runs r ON r.id = f.rule_run_id
WHERE r.review_id = $1
ORDER BY f.path, f.start_line, f.id;

-- name: OpenProductionFindingsByFingerprint :many
-- Cross-push identity (spec §6.2): playground, baseline and eval runs never
-- count as the same finding.
SELECT f.* FROM findings f
JOIN rule_runs r ON r.id = f.rule_run_id
JOIN reviews v ON v.id = r.review_id
WHERE f.repo = $1 AND f.pr_number = $2 AND f.fingerprint = $3
  AND v.caller = 'production' AND f.state = 'posted';

-- name: MarkFindingPosted :exec
UPDATE findings SET state = 'posted', gh_comment_id = $2 WHERE id = $1;

-- name: InsertModelCall :exec
INSERT INTO model_calls (review_id, rule_run_id, batch_id, repo, caller, stage, provider, model, failover,
                         data_classes, input_tokens, cached_tokens, output_tokens, cost_usd, latency_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: ReviewSpend :one
SELECT coalesce(sum(cost_usd), 0)::float8 AS spend FROM model_calls WHERE review_id = $1;

-- name: OpenFindings :many
-- A PR's posted findings that are still open, from production reviews
-- (spec §6.2: a fingerprint is posted at most once per PR).
SELECT f.id, f.rule_id, f.path, f.side, f.start_line, f.end_line, f.fingerprint, f.anchor_code, f.anchor_ordinal,
       f.gh_comment_id, f.gh_thread_id, r.head_sha, f.suggested_fix, f.fix_valid
FROM findings f
JOIN rule_runs rr ON rr.id = f.rule_run_id
JOIN reviews r ON r.id = rr.review_id
WHERE f.repo = $1 AND f.pr_number = $2 AND f.state = 'posted' AND r.caller = 'production'
ORDER BY f.id;

-- name: CloseFinding :exec
-- An open finding was addressed or its rule retired (state resolved), or
-- is no longer present (outdated).
UPDATE findings SET state = $2, gh_thread_id = coalesce($3, gh_thread_id) WHERE id = $1 AND state = 'posted';

-- name: LastCompleteReview :many
-- The PR's latest complete production review of another head: what an
-- incremental re-review diffs against (spec §6.2).
SELECT id, head_sha, base_sha FROM reviews
WHERE repo = $1 AND pr_number = $2 AND head_sha <> $3 AND caller = 'production' AND status = 'done'
ORDER BY finished_at DESC NULLS LAST, id DESC LIMIT 1;

-- name: CarryableRuns :many
-- A review's rule runs, each with the run that did the work (itself, or
-- the one it was carried from) and that run's candidate count.
SELECT rr.rule_id, rr.rule_version, rr.lane, rr.status, rr.coverage, rr.scoped_files,
       coalesce(rr.carried_from, rr.id)::bigint AS origin_id,
       (SELECT count(*) FROM findings f WHERE f.rule_run_id = coalesce(rr.carried_from, rr.id))::int AS candidates
FROM rule_runs rr WHERE rr.review_id = $1;

-- name: MoveFinding :exec
-- Re-anchored after a rebase (FR-C4): the same code now sits at these lines.
UPDATE findings SET start_line = $2, end_line = $3 WHERE id = $1 AND state = 'posted';

-- name: PostedFindingsAtHead :many
-- Late-CI linter dedupe (spec §6.10): the findings production reviews of
-- this head posted that are still open, with what the judge needs.
SELECT f.id, f.rule_id, f.lane, f.severity, f.path, f.side, f.start_line, f.end_line, f.fingerprint,
       f.message, f.gh_comment_id, f.gh_thread_id
FROM findings f
JOIN rule_runs rr ON rr.id = f.rule_run_id
JOIN reviews r ON r.id = rr.review_id
WHERE f.repo = $1 AND f.pr_number = $2 AND r.head_sha = $3 AND f.state = 'posted' AND r.caller = 'production'
  AND f.gh_comment_id IS NOT NULL
ORDER BY f.id;

-- name: MinimizeFinding :execrows
-- A posted finding a late linter also reports (spec §6.10): its comment is
-- minimized, and it counts against neither precision nor feedback.
UPDATE findings SET state = 'minimized', suppressed_reason = 'linter_duplicate' WHERE id = $1 AND state = 'posted';
