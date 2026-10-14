-- Verifier calibration labels (spec §4.3, §6.8, §8.8; PRD FR-X9): findings
-- an author marked Wrong or Fixed, and the monthly human-labeled sample.

-- +goose Up
CREATE TABLE verifier_labels (
  finding_id bigint PRIMARY KEY REFERENCES findings(id),
  label      text NOT NULL CHECK (label IN ('true', 'wrong', 'unimportant')),
  source     text NOT NULL CHECK (source IN ('author_signal', 'human_review')),
  labeled_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE verifier_labels;
