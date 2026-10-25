-- Class-of-bug grouping (spec §6.2, FR-C9): more than
-- posting.class_threshold occurrences of one rule or bug class in a run
-- post as one comment at the first. Every occurrence keeps its own row and
-- shares the group's key and, once posted, the leader's comment and thread.

-- +goose Up
ALTER TABLE findings ADD COLUMN group_key text;
CREATE INDEX findings_group_idx ON findings (repo, pr_number, group_key) WHERE group_key IS NOT NULL;

-- +goose Down
DROP INDEX findings_group_idx;
ALTER TABLE findings DROP COLUMN group_key;
