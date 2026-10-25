-- name: UpsertSeed :one
-- An owner-written seed (spec §8.1) on the config PR that carries its rule.
-- Seen again on the same PR, it takes the rule as now written; a new PR
-- revives a seed that was killed or retired. Its cases are generated
-- again for a new PR, or for a changed rule not yet delivered. A killed
-- seed stays killed while its PR is open, and an open seed stays on its
-- PR when another PR adds the same rule id: no row is returned.
INSERT INTO candidates (source_kind, ext, title, description, cluster_size, distinct_reviewers, score, score_detail,
  owner, status, config_pr_url, config_pr_branch, rule_id, rule_path, rule_yaml, seed_author)
VALUES ('owner_seed', '*', @title, @description, 0, 0, 0, '{}', @owner, 'pr_open', @config_pr_url, @config_pr_branch,
  @rule_id, @rule_path, @rule_yaml, @seed_author)
ON CONFLICT (rule_id) WHERE source_kind = 'owner_seed' DO UPDATE SET
  title = EXCLUDED.title, description = EXCLUDED.description, owner = EXCLUDED.owner, status = 'pr_open',
  rule_path = EXCLUDED.rule_path, rule_yaml = EXCLUDED.rule_yaml,
  config_pr_url = EXCLUDED.config_pr_url, config_pr_branch = EXCLUDED.config_pr_branch,
  seed_author = CASE WHEN candidates.config_pr_url IS DISTINCT FROM EXCLUDED.config_pr_url
    THEN EXCLUDED.seed_author ELSE coalesce(candidates.seed_author, EXCLUDED.seed_author) END,
  kill_reason = CASE WHEN candidates.config_pr_url IS DISTINCT FROM EXCLUDED.config_pr_url THEN NULL ELSE candidates.kill_reason END,
  seed_evals_at = CASE WHEN candidates.config_pr_url IS DISTINCT FROM EXCLUDED.config_pr_url
      OR (candidates.rule_yaml IS DISTINCT FROM EXCLUDED.rule_yaml AND candidates.seed_cases_delivered_at IS NULL)
    THEN NULL ELSE candidates.seed_evals_at END,
  seed_cases_delivered_at = CASE WHEN candidates.config_pr_url IS DISTINCT FROM EXCLUDED.config_pr_url
    THEN NULL ELSE candidates.seed_cases_delivered_at END,
  updated_at = now()
WHERE (candidates.status = 'pr_open' AND candidates.config_pr_url IS NOT DISTINCT FROM EXCLUDED.config_pr_url)
   OR (candidates.status IN ('killed', 'retired') AND candidates.config_pr_url IS DISTINCT FROM EXCLUDED.config_pr_url)
RETURNING id, status;

-- name: OpenSeed :one
-- The PR of the open seed for a rule id.
SELECT coalesce(config_pr_url, '')::text FROM candidates
WHERE source_kind = 'owner_seed' AND rule_id = $1 AND status = 'pr_open';

-- name: SeedsNeedingEvals :many
-- Open seeds whose cases have not been generated for their current rule.
SELECT id, rule_id, rule_yaml FROM candidates
WHERE source_kind = 'owner_seed' AND status = 'pr_open' AND seed_evals_at IS NULL
ORDER BY id
LIMIT $1;

-- name: DeleteCandidateEvalCases :exec
DELETE FROM candidate_eval_cases WHERE candidate_id = $1;

-- name: MarkSeedEvals :exec
UPDATE candidates SET seed_evals_at = now(), updated_at = now() WHERE id = $1;

-- name: SeedNegativePRs :many
-- Recently merged PRs a CODEOWNER approved, for a seed's negatives. A seed
-- has no source comments to exclude or repos to start from; eligibility
-- and file matching happen in code.
SELECT p.repo, p.pr_number, p.head_sha, p.base_sha, p.files
FROM mined_prs p
WHERE p.merged_at IS NOT NULL AND p.approved_by_codeowner
ORDER BY p.merged_at DESC, p.repo, p.pr_number
LIMIT $1;

-- name: SeedsToDeliver :many
-- Open seeds with generated cases not yet delivered to their PR.
SELECT id, rule_id, config_pr_url, config_pr_branch FROM candidates c
WHERE source_kind = 'owner_seed' AND status = 'pr_open' AND seed_evals_at IS NOT NULL
  AND seed_cases_delivered_at IS NULL
  AND EXISTS (SELECT 1 FROM candidate_eval_cases e WHERE e.candidate_id = c.id)
ORDER BY id;

-- name: MarkSeedDelivered :exec
UPDATE candidates SET seed_cases_delivered_at = now(), updated_at = now() WHERE id = $1;

-- name: OpenProposals :many
-- Candidates whose config PR is open, mined or seed, with what the
-- promotion sweep needs. Proposals from before rule_path was recorded are
-- left out.
SELECT id, source_kind, rule_id, rule_path, config_pr_url, config_pr_branch FROM candidates
WHERE status = 'pr_open' AND config_pr_branch IS NOT NULL AND rule_path IS NOT NULL
ORDER BY id;

-- name: MarkCandidatePromoted :execrows
UPDATE candidates SET status = 'promoted', updated_at = now() WHERE id = $1 AND status = 'pr_open';

-- name: RetireSeed :execrows
-- A seed whose PR closed without merging.
UPDATE candidates SET status = 'retired', updated_at = now()
WHERE id = $1 AND status = 'pr_open' AND source_kind = 'owner_seed';
