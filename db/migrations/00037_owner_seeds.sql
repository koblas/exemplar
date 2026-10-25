-- Owner-written seeds (spec §8.1, FR-L1.8): a rule an owner writes by
-- hand, through the playground or as a status: candidate rule file in a
-- config PR, becomes a candidates row with source_kind 'owner_seed'. A seed
-- has no cluster: it starts pr_open on the PR that carries its rule, the
-- evals job generates its cases, and the propose job delivers them to that
-- PR and marks it promoted once the rule merges as shadow or active.
-- rule_id and rule_path are recorded for every proposal, mined ones too,
-- so the same sweep promotes them.

-- +goose Up
ALTER TABLE candidates ADD COLUMN source_kind text NOT NULL DEFAULT 'comments'
  CHECK (source_kind IN ('comments', 'owner_seed'));
ALTER TABLE candidates ALTER COLUMN cluster_hash DROP NOT NULL;
ALTER TABLE candidates ALTER COLUMN last_run_id DROP NOT NULL;
ALTER TABLE candidates ADD CONSTRAINT candidates_cluster_check
  CHECK (source_kind <> 'comments' OR (cluster_hash IS NOT NULL AND last_run_id IS NOT NULL));
ALTER TABLE candidates ADD COLUMN rule_id text;
ALTER TABLE candidates ADD COLUMN rule_path text;
ALTER TABLE candidates ADD COLUMN rule_yaml text;               -- a seed's rule as written at the PR head
ALTER TABLE candidates ADD COLUMN seed_author text;             -- the person who wrote the seed (its citation)
ALTER TABLE candidates ADD COLUMN config_pr_branch text;        -- a seed's PR head branch, in exemplar-config
ALTER TABLE candidates ADD COLUMN seed_evals_at timestamptz;    -- when the evals job last generated its cases
ALTER TABLE candidates ADD COLUMN seed_cases_delivered_at timestamptz;
ALTER TABLE candidates ADD CONSTRAINT candidates_seed_check
  CHECK (source_kind <> 'owner_seed' OR (rule_id IS NOT NULL AND rule_path IS NOT NULL AND rule_yaml IS NOT NULL));
CREATE UNIQUE INDEX candidates_seed_rule_idx ON candidates (rule_id) WHERE source_kind = 'owner_seed';

-- +goose Down
DELETE FROM candidates WHERE source_kind <> 'comments';
DROP INDEX candidates_seed_rule_idx;
ALTER TABLE candidates DROP CONSTRAINT candidates_seed_check;
ALTER TABLE candidates DROP COLUMN seed_cases_delivered_at;
ALTER TABLE candidates DROP COLUMN seed_evals_at;
ALTER TABLE candidates DROP COLUMN config_pr_branch;
ALTER TABLE candidates DROP COLUMN seed_author;
ALTER TABLE candidates DROP COLUMN rule_yaml;
ALTER TABLE candidates DROP COLUMN rule_path;
ALTER TABLE candidates DROP COLUMN rule_id;
ALTER TABLE candidates DROP CONSTRAINT candidates_cluster_check;
ALTER TABLE candidates ALTER COLUMN last_run_id SET NOT NULL;
ALTER TABLE candidates ALTER COLUMN cluster_hash SET NOT NULL;
ALTER TABLE candidates DROP COLUMN source_kind;
