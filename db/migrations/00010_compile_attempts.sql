-- Compile to deterministic (spec §8.6): every attempt to turn a candidate
-- into an ast-grep or Semgrep rule, accepted or not, with its eval scores.

-- +goose Up
CREATE TABLE compile_attempts (
  id                 bigserial PRIMARY KEY,
  candidate_id       bigint NOT NULL REFERENCES candidates(id) ON DELETE CASCADE,
  attempted_at       timestamptz NOT NULL DEFAULT now(),
  model              text NOT NULL,
  engine             text CHECK (engine IN ('ast-grep', 'semgrep')),
  rule_body          text,                -- normalized rule, when one was produced
  status             text NOT NULL CHECK (status IN ('accepted', 'rejected', 'invalid', 'error')),
  reason             text NOT NULL,
  cases              int NOT NULL DEFAULT 0,
  true_pos           int NOT NULL DEFAULT 0,
  false_pos          int NOT NULL DEFAULT 0,
  false_neg          int NOT NULL DEFAULT 0,
  precision          real,
  recall             real,
  -- What precision/recall were compared against: the exemplar's own eval
  -- scores, or global.yaml eval_thresholds when the exemplar has none yet.
  baseline           text NOT NULL CHECK (baseline IN ('exemplar', 'thresholds')),
  baseline_precision real NOT NULL,
  baseline_recall    real NOT NULL,
  sandbox            text NOT NULL,       -- the wrapper engines ran under; 'none' if unsandboxed
  metrics            jsonb NOT NULL DEFAULT '{}' -- per-case outcomes
);
CREATE INDEX compile_attempts_candidate_idx ON compile_attempts (candidate_id, attempted_at DESC);

-- +goose Down
DROP TABLE compile_attempts;
