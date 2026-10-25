-- name: CandidatesNeedingEvals :many
-- New mined candidates with no generated cases yet (seeds: SeedsNeedingEvals).
SELECT c.id, c.ext, c.title, c.description
FROM candidates c
WHERE c.status = 'new' AND c.source_kind = 'comments'
  AND NOT EXISTS (SELECT 1 FROM candidate_eval_cases e WHERE e.candidate_id = c.id)
ORDER BY c.score DESC, c.id
LIMIT $1;

-- name: CandidatePositiveSources :many
SELECT m.id, m.repo, m.pr_number, m.path, m.commit_sha, m.start_line, m.end_line, m.comment_url
FROM candidate_sources s JOIN mined_comments m ON m.id = s.comment_id
WHERE s.candidate_id = $1
ORDER BY m.id;

-- name: NegativePRs :many
-- Merged PRs a CODEOWNER approved, in the candidate's repos, that drew no
-- comment from the candidate's cluster. File matching happens in code.
SELECT p.repo, p.pr_number, p.head_sha, p.base_sha, p.files
FROM mined_prs p
WHERE p.merged_at IS NOT NULL AND p.approved_by_codeowner
  AND p.repo = ANY(@repos::text[])
  AND NOT EXISTS (
    SELECT 1 FROM candidate_sources s JOIN mined_comments m ON m.id = s.comment_id
    WHERE s.candidate_id = @candidate_id AND m.repo = p.repo AND m.pr_number = p.pr_number)
ORDER BY p.merged_at DESC, p.repo, p.pr_number
LIMIT @lim;

-- name: InsertCandidateEvalCase :exec
INSERT INTO candidate_eval_cases (candidate_id, case_id, kind, name, ref, patch, expect, spot_check, paths)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (candidate_id, case_id) DO NOTHING;

-- name: CandidateEvalCases :many
SELECT case_id, kind, name, ref, patch, expect, spot_check, paths
FROM candidate_eval_cases WHERE candidate_id = $1
ORDER BY kind DESC, case_id;
