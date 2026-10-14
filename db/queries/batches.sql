-- name: InsertModelBatch :exec
INSERT INTO model_batches (batch_id, provider, model, stage, caller, repo, data_classes, request_count)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (batch_id) DO NOTHING;

-- name: GetModelBatch :one
SELECT * FROM model_batches WHERE batch_id = $1;

-- name: ClaimBatchAccounting :execrows
-- Returns 1 the first time only, so usage is booked once per batch.
UPDATE model_batches SET accounted_at = now() WHERE batch_id = $1 AND accounted_at IS NULL;
