-- name: ProposableCandidates :many
-- Highest-scoring mined candidates not yet proposed (a seed already has its PR).
SELECT id, ext, title, description, cluster_size, distinct_reviewers, score, score_detail, owner
FROM candidates
WHERE status = 'new' AND source_kind = 'comments'
ORDER BY score DESC, id
LIMIT $1;

-- name: OpenCandidatePRsByOwner :many
-- The throttle counts mined proposals only: an owner's own seeds neither
-- use up nor wait for the mined allowance.
SELECT owner, count(*)::int AS open FROM candidates WHERE status = 'pr_open' AND source_kind = 'comments' GROUP BY owner;

-- name: CandidateSourceDetails :many
-- Links and context for the candidate PR: no comment bodies, which may come
-- from repos more restricted than exemplar-config.
SELECT m.id, m.repo, m.path, m.comment_url, m.doc_urls, m.source, e.category, e.preferred_approach
FROM candidate_sources s
JOIN mined_comments m ON m.id = s.comment_id
LEFT JOIN extractions e ON e.comment_id = m.id
WHERE s.candidate_id = $1
ORDER BY m.id;

-- name: CandidateIncidents :many
SELECT DISTINCT pe.repo, pe.path, pe.kind, pe.ref
FROM candidate_sources s
JOIN mined_comments m ON m.id = s.comment_id
JOIN path_events pe ON pe.repo = m.repo AND pe.path = m.path
WHERE s.candidate_id = $1
ORDER BY pe.kind, pe.ref;

-- name: MarkCandidateProposed :execrows
-- Only a candidate still 'new' moves to pr_open, so two writers cannot both claim it.
UPDATE candidates SET status = 'pr_open', config_pr_url = $2, config_pr_branch = $3, rule_id = $4, rule_path = $5,
  updated_at = now()
WHERE id = $1 AND status = 'new';
