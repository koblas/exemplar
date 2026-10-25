-- name: CandidatesNeedingCompile :many
-- Candidates with eval cases and no scored attempt in the last 90 days
-- (every candidate, and quarterly again), with at most 3
-- attempts of any kind in that window so a hopeless one stops retrying.
-- Mined candidates only: an accepted compile shapes the PR propose drafts,
-- and a seed's owner wrote its rule and PR.
SELECT c.id, c.ext, c.title, c.description
FROM candidates c
WHERE c.status IN ('new', 'pr_open', 'promoted') AND c.source_kind = 'comments'
  AND EXISTS (SELECT 1 FROM candidate_eval_cases e WHERE e.candidate_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM compile_attempts a WHERE a.candidate_id = c.id
                  AND a.attempted_at > now() - interval '90 days' AND a.status IN ('accepted', 'rejected'))
  AND (SELECT count(*) FROM compile_attempts a WHERE a.candidate_id = c.id
       AND a.attempted_at > now() - interval '90 days') < 3
ORDER BY c.score DESC, c.id
LIMIT $1;

-- name: CompileExamples :many
-- Positive examples for the compiler: source comments' paths and hunks.
SELECT m.id, m.path, m.diff_hunk, e.preferred_approach
FROM candidate_sources s
JOIN mined_comments m ON m.id = s.comment_id
LEFT JOIN extractions e ON e.comment_id = m.id
WHERE s.candidate_id = $1 AND m.diff_hunk IS NOT NULL
ORDER BY m.thread_resolved DESC, m.id
LIMIT 5;

-- name: PRFilesAtHead :one
SELECT files FROM mined_prs WHERE repo = $1 AND head_sha = $2 LIMIT 1;

-- name: InsertCompileAttempt :one
INSERT INTO compile_attempts (candidate_id, model, engine, rule_body, status, reason, cases, true_pos, false_pos,
                              false_neg, precision, recall, baseline, baseline_precision, baseline_recall, sandbox, metrics)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
RETURNING id;

-- name: LatestAcceptedCompile :one
SELECT id, engine, rule_body, precision, recall, baseline, baseline_precision, baseline_recall, cases
FROM compile_attempts
WHERE candidate_id = $1 AND status = 'accepted'
ORDER BY attempted_at DESC
LIMIT 1;
