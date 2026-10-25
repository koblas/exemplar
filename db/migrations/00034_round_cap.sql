-- Round cap (spec §6.2, FR-C8): a round is a production run that posted
-- at least one new inline finding. Past rounds.max since the last reset,
-- new findings (but blocking and Security ones) go to one summary comment
-- as state 'summarized'. A person's review or review comment, or the PR
-- leaving draft, resets the count; round_resets records each reset.

-- +goose Up
ALTER TABLE reviews ADD COLUMN round int, ADD COLUMN round_capped boolean NOT NULL DEFAULT false;
ALTER TABLE findings DROP CONSTRAINT findings_state_check;
ALTER TABLE findings ADD CONSTRAINT findings_state_check
  CHECK (state IN ('candidate', 'posted', 'shadow', 'dropped', 'outdated', 'resolved', 'minimized', 'summarized'));
CREATE TABLE round_resets (
  id        bigserial PRIMARY KEY,
  repo      text NOT NULL,
  pr_number int NOT NULL,
  at        timestamptz NOT NULL,
  reason    text NOT NULL CHECK (reason IN ('review', 'review_comment', 'ready_for_review'))
);
CREATE INDEX round_resets_pr_idx ON round_resets (repo, pr_number, at DESC);

-- +goose Down
DROP TABLE round_resets;
UPDATE findings SET state = 'dropped' WHERE state = 'summarized';
ALTER TABLE findings DROP CONSTRAINT findings_state_check;
ALTER TABLE findings ADD CONSTRAINT findings_state_check
  CHECK (state IN ('candidate', 'posted', 'shadow', 'dropped', 'outdated', 'resolved', 'minimized'));
ALTER TABLE reviews DROP COLUMN round, DROP COLUMN round_capped;
