-- Teach-the-bot proposals (spec §4, §8.7): one per rule version and class,
-- so a recurring signal is proposed once.

-- +goose Up
CREATE TABLE teach_proposals (
  id                bigserial PRIMARY KEY,
  rule_id           text NOT NULL,
  rule_version      text NOT NULL,
  teach_class       text NOT NULL CHECK (teach_class IN ('scope', 'exception', 'rule_wrong', 'disagree')),
  evidence_findings bigint[] NOT NULL,
  config_pr_url     text,
  state             text NOT NULL CHECK (state IN ('proposed', 'issue')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (rule_id, rule_version, teach_class)
);

-- +goose Down
DROP TABLE teach_proposals;
