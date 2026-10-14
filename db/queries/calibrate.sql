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
