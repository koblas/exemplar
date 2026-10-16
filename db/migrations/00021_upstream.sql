-- Upstream effectiveness: per exported rule,
-- violations per in-scope PR when the PR's coding agent received the
-- rule's export versus when it did not, and in exporting repos before
-- versus after the export landed.

-- +goose Up
-- The coding agent a production review's PR was written with ('' none,
-- NULL not recorded), and the export targets that agent reads.
ALTER TABLE reviews ADD COLUMN agent text, ADD COLUMN agent_reads text[];
-- When the file first matched its rendering on the default branch.
ALTER TABLE exports ADD COLUMN landed_at timestamptz;
UPDATE exports SET landed_at = current_at WHERE state = 'current';

-- One row per exported rule and in-scope PR (a production rule run with
-- scoped files). Violations are distinct findings that were not held back
-- as wrong or unimportant: posted, shadow, or withheld only because
-- someone or something else already raised them.
CREATE VIEW upstream_pr_rule AS
WITH runs AS (
  SELECT rr.rule_id, r.repo, r.pr_number, min(r.started_at) AS first_review, array_agg(rr.id) AS run_ids
  FROM rule_runs rr
  JOIN reviews r ON r.id = rr.review_id
  WHERE r.caller = 'production' AND cardinality(rr.scoped_files) > 0 AND r.started_at IS NOT NULL
    AND EXISTS (SELECT 1 FROM rules ru WHERE ru.rule_id = rr.rule_id AND ru.version_sha = rr.rule_version AND ru.exported)
  GROUP BY rr.rule_id, r.repo, r.pr_number
)
SELECT u.rule_id, u.repo, u.pr_number, u.first_review, coalesce(a.agent, '') AS agent,
  coalesce(a.agent, '') <> '' AND EXISTS (SELECT 1 FROM exports e WHERE e.repo = u.repo AND e.target = ANY(a.agent_reads)
    AND e.landed_at <= u.first_review) AS received,
  (SELECT min(e.landed_at) FROM exports e WHERE e.repo = u.repo) AS export_landed,
  (SELECT count(DISTINCT f.fingerprint) FROM findings f WHERE f.rule_run_id = ANY(u.run_ids)
    AND (f.state IN ('posted', 'shadow', 'resolved', 'outdated', 'minimized')
         OR f.suppressed_reason IN ('already_posted', 'repo_shadow', 'linter_duplicate', 'human_duplicate', 'duplicate', 'budget')))::int AS violations
FROM runs u
-- The PR's agent, as its latest production review recorded it.
LEFT JOIN LATERAL (
  SELECT r.agent, r.agent_reads FROM reviews r
  WHERE r.repo = u.repo AND r.pr_number = u.pr_number AND r.caller = 'production' AND r.agent IS NOT NULL
  ORDER BY r.id DESC LIMIT 1) a ON true;

-- +goose Down
DROP VIEW upstream_pr_rule;
ALTER TABLE exports DROP COLUMN landed_at;
ALTER TABLE reviews DROP COLUMN agent_reads, DROP COLUMN agent;
