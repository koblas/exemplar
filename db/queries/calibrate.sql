-- name: VerifierCalibrationLabels :many
-- Labeled findings with the PR they were found on. "unimportant" labels
-- calibrate importance, not refutation, and are not returned.
SELECT f.id, l.label, f.rule_id, f.path, f.side, f.start_line, f.end_line, f.message,
       r.repo, r.pr_number, r.base_sha, r.head_sha
FROM verifier_labels l
JOIN findings f ON f.id = l.finding_id
JOIN rule_runs rr ON rr.id = f.rule_run_id
JOIN reviews r ON r.id = rr.review_id
WHERE l.label IN ('true', 'wrong')
ORDER BY f.id
LIMIT $1;

-- name: RecordCalibration :exec
INSERT INTO calibration_results (kind, key, config_version, providers, known_wrong_refuted, known_true_refuted, labels, passed)
VALUES (@kind, @key, @config_version, @providers, @known_wrong_refuted, @known_true_refuted, @labels, @passed);

-- name: LatestCalibration :one
-- The latest result for kind and key decides; a later failure re-gates.
SELECT passed, ran_at FROM calibration_results WHERE kind = @kind AND key = @key ORDER BY ran_at DESC, id DESC LIMIT 1;
