## 4. Data layer

### 4.1 Stores

| Data | Store |
|---|---|
| Rules, eval cases, repo enablement, kill switches | **Git** : `exemplar-config` and target repos' `.exemplar/` (source of truth) |
| Mirror of active rules by git SHA | Postgres `rules` |
| Webhook events, reviews, rule runs, findings, feedback, gate traces | Postgres |
| Mined comments, extractions, embeddings ( `pgvector` ), clusters, candidates | Postgres |
| Job queue | Postgres (River) |
| Cost ledger | Postgres |
| Model transcripts, raw diffs, eval artifacts | Object storage (S3/GCS); Postgres stores keys |
| Git mirror and code-search index | `exemplar-search` local SSD volumes (rebuildable from GitHub) |
| Traces and telemetry | OTEL collector → the organization's APM backend |

### 4.2 Sizing

- **Embeddings:** assume 1.7M comments × 10–15% extraction rate ≈ 170K–255K extractions. At 1024 dims as `halfvec` (2 bytes/dim) that is ~0.5 GB; even embedding all 1.7M raw comments at float32 is ~7 GB. Comfortably inside one Postgres with an HNSW index.
- **Findings:** at 2,000 PRs/day, ~3 findings per PR, ~2.2M rows/year. Trivial; partition `findings` and `rule_runs` by month only when they pass ~50M rows.
- **Instance:** start with a managed Postgres (RDS / Cloud SQL / Azure Flexible) at 4 vCPU / 16 GB, 200 GB storage, one read replica for analytics and the playground.

### 4.3 Schema

```sql
CREATE EXTENSION IF NOT EXISTS vector;

-- ---------- Intake ----------
CREATE TABLE gh_events (
  delivery_id     uuid PRIMARY KEY,                 -- X-GitHub-Delivery
  event           text NOT NULL,
  action          text,
  installation_id bigint NOT NULL,
  repo_full_name  text,
  payload         jsonb NOT NULL,
  received_at     timestamptz NOT NULL DEFAULT now(),
  processed_at    timestamptz                       -- payload nulled by the retention job (§13.1, default 30 days)
);

-- ---------- Rules (mirror of git) ----------
CREATE TABLE rules (
  rule_id      text NOT NULL,
  version_sha  text NOT NULL,                       -- git SHA of the rule file
  scope        text NOT NULL,                       -- 'global' | '<org>/<repo>'
  kind         text NOT NULL,                       -- exemplar | compiled | bug_class
  category     text NOT NULL,
  severity     text NOT NULL,                       -- blocking | important | suggestion
  status       text NOT NULL,                       -- candidate | shadow | active | killed
  owner        text NOT NULL,
  fix_policy   text NOT NULL DEFAULT 'none',
  review_by    date,
  exported     boolean NOT NULL DEFAULT false,
  model_tier   text NOT NULL DEFAULT 'standard',
  spec         jsonb NOT NULL,                      -- full rule
  PRIMARY KEY (rule_id, version_sha)
);

-- ---------- Reviews ----------
CREATE TABLE reviews (
  id             bigserial PRIMARY KEY,
  repo           text NOT NULL,
  pr_number      int  NOT NULL,
  head_sha       text NOT NULL,
  base_sha       text NOT NULL,
  prev_review_id bigint REFERENCES reviews(id),
  config_version text NOT NULL,                     -- exemplar-config commit
  search_snapshot text,                             -- exemplar-search snapshot SHA used
  caller         text NOT NULL,                     -- production|playground|cli|agent|eval|baseline (mcp reserved, Appendix C)
  status         text NOT NULL,                     -- queued|running|done|incomplete|failed|superseded
  coverage       real,                              -- completed in-scope rules ÷ in-scope rules
  degraded       boolean NOT NULL DEFAULT false,
  shed           jsonb,                             -- what was shed/deferred/excluded and why
  incomplete_reason text,
  verdict        text,                              -- APPROVE | NO_OP
  bug_lane       boolean NOT NULL DEFAULT false,
  round          int,                               -- 1.. when this run posted a new finding; NULL otherwise (FR-C8, §6.2)
  round_capped   boolean NOT NULL DEFAULT false,    -- inline posting suppressed by rounds.max
                                                    -- As built: resets are rows in round_resets (repo, pr_number, at, reason: review | review_comment | ready_for_review), written by event routing
  cost_usd       numeric(10,4) NOT NULL DEFAULT 0,
  started_at     timestamptz,
  finished_at    timestamptz,
  attempt        int NOT NULL DEFAULT 1,            -- incremented by Re-run of an incomplete review
  UNIQUE (repo, pr_number, head_sha, config_version, caller, attempt)
);

-- ---------- Runners (§2.4 C1–C3) ----------
CREATE TABLE config_snapshots (
  version      text PRIMARY KEY,                    -- exemplar-config commit
  files        jsonb NOT NULL,                      -- the validated tree (path -> base64 contents); the runner re-loads it
  loaded_at    timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE runner_claims (                        -- one row per dispatched job; a retry replaces it
  job_id         bigint PRIMARY KEY,                -- River job
  attempt        int NOT NULL,
  nonce_hash     bytea NOT NULL,                    -- sha256 of the one-time nonce the runner presents once
  tokens         bytea,                             -- AES-GCM sealed grant (repo, installation, flavor, token, expiry); NULLed on exit
  config_version text NOT NULL REFERENCES config_snapshots(version),
  created_at     timestamptz NOT NULL DEFAULT now(),
  claimed_at     timestamptz, released_at timestamptz,
  expires_at     timestamptz NOT NULL               -- the earliest token expiry
);

CREATE TABLE rule_runs (
  id             bigserial PRIMARY KEY,
  review_id      bigint NOT NULL REFERENCES reviews(id),
  rule_id        text NOT NULL,                     -- rule or bug class id
  rule_version   text NOT NULL,
  lane           text NOT NULL,                     -- compiled | exemplar | bug | baseline
  scoped_files   text[] NOT NULL,
  status         text NOT NULL,                     -- ok|zero_coverage|schema_error|timeout|shed|deferred|degraded_skip
  coverage       real,
  context_degraded boolean NOT NULL DEFAULT false,
  candidates_by_severity jsonb,                     -- {blocking, important, suggestion, security} before verification; carried runs feed Stage 1 (FR-A13). As built: not a column; carried runs count findings.severity of the origin run, Security by the rule's category
  expected_value real,                              -- the FR-E16 score at scope time; NULL for Lane 1 and 3
  model text, input_tokens int, cached_tokens int, output_tokens int,
  cost_usd       numeric(10,4),
  latency_ms     int,
  transcript_key text,
  UNIQUE (review_id, rule_id, rule_version)
);

CREATE TABLE findings (
  id                bigserial PRIMARY KEY,
  rule_run_id       bigint NOT NULL REFERENCES rule_runs(id),
  repo              text NOT NULL,
  pr_number         int  NOT NULL,
  rule_id           text NOT NULL,
  lane              text NOT NULL,
  severity          text NOT NULL,
  cc_label          text NOT NULL,                  -- e.g. 'suggestion (performance)'
  path              text NOT NULL,
  side              text NOT NULL,                  -- RIGHT | LEFT
  start_line        int  NOT NULL,
  end_line          int  NOT NULL,
  fingerprint       text NOT NULL,
  message           text NOT NULL,
  verify_verdict    text,                           -- confirmed | refuted | uncertain | NULL (compiled)
  verify_importance real,
  verify_model      text,
  verify_same_family boolean NOT NULL DEFAULT false,
  verify_evidence   jsonb,
  verify_quorum_n   int,                            -- FR-V7: verifiers asked (NULL = single verifier)
  verify_quorum_agreed int,                         -- how many returned confirmed; share = agreed / n
  group_key         text,                           -- FR-C9: set on every occurrence of a class-of-bug group; one thread per key
  suggested_fix     text,
  fix_valid         boolean,
  rank_score        real,
  state             text NOT NULL,                  -- posted | shadow | dropped | outdated | resolved | minimized | summarized
  suppressed_reason text,                           -- refuted|uncertain|unimportant|bad_evidence|linter_duplicate|human_duplicate|dismissed|budget|duplicate|quorum|quorum_shed|round_cap|importance_prior
  gh_comment_id     bigint,
  gh_thread_id      text,
  sarif_alert_number int,
  created_at        timestamptz NOT NULL DEFAULT now(),
  -- fingerprint includes an occurrence index (§6.2), so identical code at two anchors yields two findings.
  -- Uniqueness is per review: playground, baseline and eval runs on the same PR never collide with production.
  UNIQUE (rule_run_id, fingerprint)
);
CREATE INDEX ON findings (repo, pr_number, fingerprint);  -- cross-push identity lookups (§6.2)

CREATE TABLE feedback_events (
  id          bigserial PRIMARY KEY,
  finding_id  bigint NOT NULL REFERENCES findings(id),
  kind        text NOT NULL,   -- fb_fixed|fb_wrong|fb_not_important|fb_handled|fb_disagree|applied_suggestion|fixed|
                               -- reply_agree|reply_disagree|reply_question|react_up|react_down|resolved|ignored|
                               -- human_agreed|adjudicated_uphold|adjudicated_overturn
  actor       text NOT NULL,
  role        text NOT NULL,   -- author | reviewer | owner | observer (§9.1)
  weight      real NOT NULL,   -- 1 | 2 | 4 | 0 by role; 0.5 for reactions (§9.2)
  via         text NOT NULL,   -- link|slash|reaction|webhook|code_scanning|inferred
  reason_text text,
  teach_class text,            -- scope|exception|rule_wrong|noise
  evidence    jsonb,
  observed_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (finding_id, kind, actor)
);

CREATE TABLE approval_decisions (
  review_id    bigint PRIMARY KEY REFERENCES reviews(id),
  stage1       jsonb NOT NULL,  -- every input and reason, incl. candidates by severity, author merges per area, owner areas, suggestion findings posted (FR-A13)
  stage2       jsonb,
  decision     text NOT NULL,  -- approved|ineligible|blocked_compliance|killed
  base_sha     text NOT NULL,  -- the base the gates were judged against (FR-A9); drift re-runs them
  revoked_at   timestamptz, revoked_reason text,   -- push | base_drift | base_touched_paths | config
  gh_review_id bigint
);

CREATE TABLE approval_failures (   -- FR-A12: an incident, revert or confirmed bug on an auto-approved PR, charged to the system
  id bigserial PRIMARY KEY,
  review_id   bigint NOT NULL REFERENCES approval_decisions(review_id),
  kind        text NOT NULL,     -- incident | revert | confirmed_bug
  ref         text NOT NULL,     -- incident id, reverting PR, or issue
  should_have_caught text,       -- gate | lane | rule:<id> | class:<id> | verifier | unknown, set at triage
  fix_ref     text,              -- config PR, eval case or code change that closed it
  recorded_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (review_id, kind, ref)
);

CREATE TABLE calibration_results (  -- verifier and categorizer calibration runs (FR-X9); gates same_family (§6.13). As built: keyed by `key`, a hash of the verifier's (or categorizer's) own inputs, instead of config_sha, so rule edits do not re-gate; written by `exemplar-learn calibrate`
  id bigserial PRIMARY KEY,
  kind         text NOT NULL,       -- verifier | categorizer
  config_sha   text NOT NULL,
  providers    text[] NOT NULL,     -- provider:model per side, as configured
  known_wrong_refuted real, known_true_refuted real,
  passed       boolean NOT NULL,
  ran_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE path_events (     -- incidents and reverts
  repo text NOT NULL, path text NOT NULL, kind text NOT NULL,   -- incident | revert
  ref  text NOT NULL, at timestamptz NOT NULL,
  PRIMARY KEY (repo, path, kind, ref)
);

-- ---------- Learning plane ----------
CREATE TABLE mined_comments (
  id              bigint PRIMARY KEY,               -- GitHub comment id
  repo text, pr_number int, path text, ext text,
  reviewer_pid text, author_pid text, created_at timestamptz,   -- HMAC pseudonyms (§13.1)
  comment_url text,
  body text, diff_hunk text, doc_urls text[],                     -- body/diff_hunk purged when uncited (§13.1)
  input_hash text,                                                -- extraction cache key (§8.2)
  is_thread_root  boolean, thread_resolved boolean,
  source          text NOT NULL,                    -- human | accepted_bot_finding | revert_seed
  extract_status  text NOT NULL DEFAULT 'pending'   -- pending|in_batch|accepted|rejected|error
);

CREATE TABLE extractions (
  comment_id        bigint PRIMARY KEY REFERENCES mined_comments(id),
  title text, description text, body text, category text,
  language_agnostic boolean NOT NULL DEFAULT false,
  extractor_model   text, extractor_version text,
  embedding         halfvec(1024)
);
CREATE INDEX ON extractions USING hnsw (embedding halfvec_cosine_ops);

CREATE TABLE candidates (
  id bigserial PRIMARY KEY,
  ext text NOT NULL, title text, description text,
  source_kind text NOT NULL DEFAULT 'comments',     -- comments | incident | revert | owner_seed | change_cluster (FR-L1.7–L1.9, co-equal)
  cluster_size int, distinct_reviewers int, score real,
  owner text, status text NOT NULL,                 -- new|pr_open|promoted|killed
  config_pr_url text, kill_reason text
);
CREATE TABLE candidate_sources (
  candidate_id bigint, comment_id bigint, PRIMARY KEY (candidate_id, comment_id)
);

CREATE TABLE compile_attempts (
  id bigserial PRIMARY KEY, candidate_id bigint, rule_id text,
  engine text, rule_body text, precision real, recall real,
  accepted boolean, attempted_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE eval_cases (
  rule_id text, case_id text, synthetic boolean NOT NULL DEFAULT false,
  adversarial boolean NOT NULL DEFAULT false,
  ref text NOT NULL, patch text, expect jsonb NOT NULL,
  PRIMARY KEY (rule_id, case_id)
);

CREATE TABLE clean_prs (           -- FR-X12: PRs known to be clean; every finding on them is a false alarm
  repo text, pr_number int, head_sha text NOT NULL,
  confirmed_by text NOT NULL, confirmed_at timestamptz NOT NULL,   -- an engineer who knows the code
  PRIMARY KEY (repo, pr_number)
);
CREATE TABLE clean_pr_runs (
  id bigserial PRIMARY KEY, ran_at timestamptz NOT NULL DEFAULT now(),
  config_sha text NOT NULL, prompts_sha text NOT NULL, providers text[] NOT NULL,
  false_alarms int NOT NULL, harmful int NOT NULL,                -- harmful: a false alarm carrying a suggested fix
  by_rule jsonb NOT NULL                                            -- {rule_or_class: {false_alarms, harmful}}
);

CREATE TABLE verifier_labels (
  finding_id bigint PRIMARY KEY REFERENCES findings(id),
  label      text NOT NULL,                         -- true | wrong | unimportant
  source     text NOT NULL,                         -- author_signal | human_review
  labeled_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE teach_proposals (
  id bigserial PRIMARY KEY, rule_id text, teach_class text,
  evidence_findings bigint[], config_pr_url text, state text,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- ---------- Shift left ----------
CREATE TABLE exports (
  repo text, path text, target text,                -- agents_md | claude_md | copilot | cursor
  content_hash text, config_sha text, pr_url text, state text, auto_merged boolean, updated_at timestamptz,
  PRIMARY KEY (repo, path)
);

-- ---------- Cost ----------
CREATE TABLE model_calls (           -- cost accounting and data-egress log (§6.14, §13.1); 90-day retention
  id bigserial PRIMARY KEY, at timestamptz NOT NULL DEFAULT now(),
  review_id bigint, rule_run_id bigint, repo text,
  caller text,        -- production|playground|cli|agent|eval|baseline|learning (mcp reserved)
  stage text,         -- generator|verifier|light|categorizer|extract|merge|compile|embed
  provider text, model text, failover boolean NOT NULL DEFAULT false,
  data_classes text[] NOT NULL,       -- diff|file_context|search_context|ticket|review_comments|pr_text
  input_tokens int, cached_tokens int, output_tokens int, cost_usd numeric(10,6), latency_ms int
);
```

`findings` and `rule_runs` are partitioned by month once they exceed ~50M rows. Repository context adds no tables; its data lives on `exemplar-search` volumes.

---
