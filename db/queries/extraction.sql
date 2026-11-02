-- name: InsertMinedComment :exec
-- Used by ingest (and tests). Idempotent on the GitHub comment id.
INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at,
                            comment_url, body, diff_hunk, doc_urls, is_thread_root, thread_resolved, source)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (id) DO NOTHING;

-- name: PendingComments :many
SELECT id, repo, path, ext, body, diff_hunk, source, extract_attempts FROM mined_comments
WHERE extract_status = 'pending' AND body IS NOT NULL AND author_trusted IS NOT FALSE
ORDER BY id
LIMIT $1;

-- name: SetCommentHash :exec
UPDATE mined_comments SET input_hash = $2 WHERE id = $1;

-- name: MarkCommentsInBatch :exec
UPDATE mined_comments SET extract_status = 'in_batch', extract_batch_id = @batch_id::text
WHERE id = ANY(@ids::bigint[]) AND extract_status = 'pending';

-- name: ReturnCommentsToPending :exec
-- A failed request sends its comments back; after max attempts they stop
-- at 'error' with the reason, never silently dropped.
UPDATE mined_comments
SET extract_attempts = extract_attempts + 1,
    extract_status = CASE WHEN extract_attempts + 1 >= @max_attempts::int THEN 'error' ELSE 'pending' END,
    extract_error = @reason::text,
    extract_batch_id = NULL
WHERE id = ANY(@ids::bigint[]) AND extract_status IN ('pending', 'in_batch');

-- name: FailComment :exec
UPDATE mined_comments SET extract_status = 'error', extract_error = $2, extract_batch_id = NULL WHERE id = $1;

-- name: FinishComment :exec
UPDATE mined_comments SET extract_status = $2, extract_error = NULL, extract_batch_id = NULL WHERE id = $1;

-- name: CommentsInBatch :many
SELECT id, input_hash FROM mined_comments WHERE extract_batch_id = $1 AND extract_status = 'in_batch';

-- name: UpsertExtraction :exec
INSERT INTO extractions (comment_id, title, description, preferred_approach, applies_to_hint, category,
                         language_agnostic, confidence, extractor_model, extractor_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (comment_id) DO UPDATE SET
  title = EXCLUDED.title, description = EXCLUDED.description, preferred_approach = EXCLUDED.preferred_approach,
  applies_to_hint = EXCLUDED.applies_to_hint, category = EXCLUDED.category,
  language_agnostic = EXCLUDED.language_agnostic, confidence = EXCLUDED.confidence,
  extractor_model = EXCLUDED.extractor_model, extractor_version = EXCLUDED.extractor_version, embedding = NULL,
  embedding_model = NULL;

-- name: GetCachedExtraction :one
SELECT result FROM extraction_cache WHERE input_hash = $1;

-- name: PutCachedExtraction :exec
INSERT INTO extraction_cache (input_hash, extractor_version, model, result)
VALUES ($1, $2, $3, $4)
ON CONFLICT (input_hash) DO NOTHING;

-- name: InsertExtractionBatch :exec
INSERT INTO extraction_batches (batch_id, extractor_version, model, comment_count)
VALUES ($1, $2, $3, $4);

-- name: OpenExtractionBatches :many
SELECT batch_id, extractor_version, model FROM extraction_batches WHERE status = 'submitted' ORDER BY submitted_at;

-- name: FinishExtractionBatch :exec
UPDATE extraction_batches
SET status = $2, processed_at = now(), accepted = $3, rejected = $4, returned = $5
WHERE batch_id = $1;

-- name: ExtractionStatusCounts :many
SELECT extract_status, count(*)::int AS n FROM mined_comments GROUP BY extract_status ORDER BY extract_status;

-- name: ParkIneligibleComment :exec
UPDATE mined_comments SET extract_status = 'ineligible', extract_error = NULL, extract_batch_id = NULL WHERE id = $1;

-- name: IneligibleCommentRepos :many
SELECT DISTINCT repo FROM mined_comments WHERE extract_status = 'ineligible' ORDER BY repo;

-- name: ReleaseIneligibleComments :execrows
-- The repo is eligible again: its parked comments are pending once more.
UPDATE mined_comments SET extract_status = 'pending' WHERE repo = $1 AND extract_status = 'ineligible';
