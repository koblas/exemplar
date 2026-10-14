-- merge_safety_score (spec §6.13) reads mined_prs by repo and merge time
-- for path churn.

-- +goose Up
CREATE INDEX mined_prs_repo_merged_idx ON mined_prs (repo, merged_at);

-- +goose Down
DROP INDEX mined_prs_repo_merged_idx;
