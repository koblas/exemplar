-- Retention (spec §13.1, FR-H7): each query handles one batch of at most
-- @batch rows older than @cutoff, so a large backlog never holds a long
-- lock; the job repeats until a batch comes back short.

-- name: NullExpiredPayloads :execrows
-- The row stays: it is the delivery's idempotency key.
UPDATE gh_events SET payload = NULL
WHERE delivery_id IN (
  SELECT e.delivery_id FROM gh_events e WHERE e.payload IS NOT NULL AND e.received_at < @cutoff LIMIT @batch
);

-- name: DeleteExpiredModelCalls :execrows
DELETE FROM model_calls WHERE id IN (SELECT m.id FROM model_calls m WHERE m.at < @cutoff LIMIT @batch);

-- name: DeleteExpiredFindings :execrows
-- Calibration labels and feedback events name a finding and go with it
-- (spec §13.1 retains findings and feedback events together).
WITH doomed AS (SELECT f.id FROM findings f WHERE f.created_at < @cutoff LIMIT @batch),
     labels AS (DELETE FROM verifier_labels WHERE finding_id IN (SELECT id FROM doomed)),
     events AS (DELETE FROM feedback_events WHERE finding_id IN (SELECT id FROM doomed))
DELETE FROM findings WHERE id IN (SELECT id FROM doomed);

-- name: DeleteExpiredApprovalDecisions :execrows
-- A decision is as old as its review.
DELETE FROM approval_decisions WHERE review_id IN (
  SELECT d.review_id FROM approval_decisions d JOIN reviews r ON r.id = d.review_id
  WHERE coalesce(r.finished_at, r.started_at) < @cutoff LIMIT @batch
);

-- name: DeleteExpiredEvaluations :execrows
-- Evaluation results carry finding text, so they keep findings' period.
DELETE FROM evaluations WHERE id IN (SELECT e.id FROM evaluations e WHERE e.created_at < @cutoff LIMIT @batch);

-- name: NullStaleEvaluationDiffs :execrows
-- A diff whose job never finished is code no one needs.
UPDATE evaluations SET diff = NULL, status = 'failed', reason = 'the review never ran', finished_at = now()
WHERE id IN (SELECT e.id FROM evaluations e WHERE e.diff IS NOT NULL AND e.created_at < @cutoff LIMIT @batch);

-- name: DeleteExpiredPlaygroundRuns :execrows
-- Playground results carry finding text, so they keep findings' period.
DELETE FROM playground_runs WHERE id IN (SELECT p.id FROM playground_runs p WHERE p.created_at < @cutoff LIMIT @batch);
