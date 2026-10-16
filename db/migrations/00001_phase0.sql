-- Core review schema. Learning-plane, feedback and export tables arrive
-- in the later migrations that write them.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;

-- ---------- Intake ----------
CREATE TABLE gh_events (
  delivery_id     uuid PRIMARY KEY,                 -- X-GitHub-Delivery
  event           text NOT NULL,
  action          text,
  installation_id bigint NOT NULL,
  repo_full_name  text,
  payload         jsonb,                            -- nulled by the retention job; the row stays for idempotency
  received_at     timestamptz NOT NULL DEFAULT now(),
  processed_at    timestamptz
);

-- ---------- Rules (mirror of git) ----------
CREATE TABLE rules (
  rule_id      text NOT NULL,
  version_sha  text NOT NULL,                       -- git blob id of the rule file
  scope        text NOT NULL,                       -- 'global' | '<org>/<repo>'
  kind         text NOT NULL CHECK (kind IN ('exemplar', 'compiled', 'bug_class')),
  category     text NOT NULL,
  severity     text NOT NULL CHECK (severity IN ('blocking', 'important', 'suggestion')),
  status       text NOT NULL CHECK (status IN ('candidate', 'shadow', 'active', 'killed')),
  owner        text NOT NULL,
  fix_policy   text NOT NULL DEFAULT 'none',
  review_by    date,
  exported     boolean NOT NULL DEFAULT false,
  model_tier   text NOT NULL DEFAULT 'standard',
  spec         jsonb NOT NULL,
  PRIMARY KEY (rule_id, version_sha)
);

-- ---------- Reviews ----------
CREATE TABLE reviews (
  id                bigserial PRIMARY KEY,
  repo              text NOT NULL,
  pr_number         int  NOT NULL,
  head_sha          text NOT NULL,
  base_sha          text NOT NULL,
  prev_review_id    bigint REFERENCES reviews(id),
  config_version    text NOT NULL,
  search_snapshot   text,
  caller            text NOT NULL CHECK (caller IN ('production', 'playground', 'cli', 'agent', 'eval', 'baseline', 'mcp')),
  status            text NOT NULL CHECK (status IN ('queued', 'running', 'done', 'incomplete', 'failed', 'superseded')),
  coverage          real,
  degraded          boolean NOT NULL DEFAULT false,
  shed              jsonb,
  incomplete_reason text,
  verdict           text CHECK (verdict IN ('APPROVE', 'NO_OP')),
  bug_lane          boolean NOT NULL DEFAULT false,
  cost_usd          numeric(10,4) NOT NULL DEFAULT 0,
  started_at        timestamptz,
  finished_at       timestamptz,
  attempt           int NOT NULL DEFAULT 1,
  UNIQUE (repo, pr_number, head_sha, config_version, caller, attempt)
);

CREATE TABLE rule_runs (
  id               bigserial PRIMARY KEY,
  review_id        bigint NOT NULL REFERENCES reviews(id),
  rule_id          text NOT NULL,
  rule_version     text NOT NULL,
  lane             text NOT NULL CHECK (lane IN ('compiled', 'exemplar', 'bug', 'baseline', 'secrets')),
  scoped_files     text[] NOT NULL,
  status           text NOT NULL CHECK (status IN ('pending', 'ok', 'zero_coverage', 'schema_error', 'timeout', 'shed', 'deferred', 'degraded_skip', 'error')),
  coverage         real,
  context_degraded boolean NOT NULL DEFAULT false,
  model            text,
  input_tokens     int,
  cached_tokens    int,
  output_tokens    int,
  cost_usd         numeric(10,4),
  latency_ms       int,
  transcript_key   text,
  UNIQUE (review_id, rule_id, rule_version)
);

CREATE TABLE findings (
  id                 bigserial PRIMARY KEY,
  rule_run_id        bigint NOT NULL REFERENCES rule_runs(id),
  repo               text NOT NULL,
  pr_number          int  NOT NULL,
  rule_id            text NOT NULL,
  lane               text NOT NULL,
  severity           text NOT NULL,
  cc_label           text NOT NULL,
  path               text NOT NULL,
  side               text NOT NULL CHECK (side IN ('RIGHT', 'LEFT')),
  start_line         int  NOT NULL,
  end_line           int  NOT NULL,
  fingerprint        text NOT NULL,
  message            text NOT NULL,
  verify_verdict     text CHECK (verify_verdict IN ('confirmed', 'refuted', 'uncertain')),
  verify_importance  real,
  verify_model       text,
  verify_same_family boolean NOT NULL DEFAULT false,
  verify_evidence    jsonb,
  suggested_fix      text,
  fix_valid          boolean,
  rank_score         real,
  state              text NOT NULL CHECK (state IN ('candidate', 'posted', 'shadow', 'dropped', 'outdated', 'resolved', 'minimized')),
  suppressed_reason  text,
  gh_comment_id      bigint,
  gh_thread_id       text,
  sarif_alert_number int,
  created_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (rule_run_id, fingerprint)
);
CREATE INDEX findings_identity_idx ON findings (repo, pr_number, fingerprint);

CREATE TABLE approval_decisions (
  review_id    bigint PRIMARY KEY REFERENCES reviews(id),
  stage1       jsonb NOT NULL,
  stage2       jsonb,
  decision     text NOT NULL CHECK (decision IN ('approved', 'ineligible', 'blocked_compliance', 'killed')),
  gh_review_id bigint
);

CREATE TABLE eval_cases (
  rule_id     text NOT NULL,
  case_id     text NOT NULL,
  synthetic   boolean NOT NULL DEFAULT false,
  adversarial boolean NOT NULL DEFAULT false,
  ref         text NOT NULL,
  patch       text,
  expect      jsonb NOT NULL,
  PRIMARY KEY (rule_id, case_id)
);

-- ---------- Cost and data egress ----------
CREATE TABLE model_calls (
  id            bigserial PRIMARY KEY,
  at            timestamptz NOT NULL DEFAULT now(),
  review_id     bigint,
  rule_run_id   bigint,
  repo          text,
  caller        text,
  stage         text NOT NULL,
  provider      text NOT NULL,
  model         text NOT NULL,
  failover      boolean NOT NULL DEFAULT false,
  data_classes  text[] NOT NULL,
  input_tokens  int,
  cached_tokens int,
  output_tokens int,
  cost_usd      numeric(10,6),
  latency_ms    int
);
CREATE INDEX model_calls_review_idx ON model_calls (review_id);

-- +goose Down
DROP TABLE model_calls;
DROP TABLE eval_cases;
DROP TABLE approval_decisions;
DROP TABLE findings;
DROP TABLE rule_runs;
DROP TABLE reviews;
DROP TABLE rules;
DROP TABLE gh_events;
