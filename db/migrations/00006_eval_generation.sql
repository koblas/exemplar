-- Eval generation (spec §8.8). Ingest records where each mined comment
-- points (the commit it was left on and its line range) and the PRs it saw,
-- so cases can reference code as repo@sha:path#Lstart-Lend without copying
-- it. Generated cases are kept per candidate because a candidate's title,
-- and so its rule id, can change until it is proposed.

-- +goose Up
ALTER TABLE mined_comments
  ADD COLUMN commit_sha text,                         -- commit the comment was left on
  ADD COLUMN start_line int,                          -- commented range on the RIGHT side
  ADD COLUMN end_line   int;

-- PRs seen by ingest; negatives come from merged PRs a CODEOWNER approved.
CREATE TABLE mined_prs (
  repo                  text NOT NULL,
  pr_number             int  NOT NULL,
  head_sha              text NOT NULL,
  base_sha              text NOT NULL,
  merged_at             timestamptz,
  approved_by_codeowner boolean NOT NULL DEFAULT false,
  files                 text[] NOT NULL,
  PRIMARY KEY (repo, pr_number)
);

CREATE TABLE candidate_eval_cases (
  candidate_id bigint NOT NULL REFERENCES candidates(id) ON DELETE CASCADE,
  case_id      text   NOT NULL,
  kind         text   NOT NULL CHECK (kind IN ('positive', 'negative', 'injected')),
  name         text   NOT NULL,
  ref          text   NOT NULL,                       -- repo@sha
  patch        text,                                  -- injected cases only
  expect       jsonb  NOT NULL,                       -- [{path, lines: [start, end]}], [] for negatives
  spot_check   boolean NOT NULL DEFAULT false,        -- owner reviews this synthetic case (10%)
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (candidate_id, case_id)
);

-- +goose Down
DROP TABLE candidate_eval_cases;
DROP TABLE mined_prs;
ALTER TABLE mined_comments DROP COLUMN end_line, DROP COLUMN start_line, DROP COLUMN commit_sha;
