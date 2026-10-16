-- Clustering output, written by the Python cluster job. Each run
-- is a snapshot: clustering is recomputed over all embedded extractions,
-- while candidates built from clusters (by the rank job) persist across runs.
-- A cluster's identity is the hash of its sorted member comment ids.

-- +goose Up
CREATE TABLE cluster_runs (
  id          bigserial PRIMARY KEY,
  started_at  timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  status      text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'failed')),
  params      jsonb NOT NULL,
  extractions int,
  clusters    int,
  merges      int
);

CREATE TABLE clusters (
  run_id             bigint NOT NULL REFERENCES cluster_runs(id) ON DELETE CASCADE,
  cluster_hash       text NOT NULL,
  partition          text NOT NULL,                 -- file extension, or '*' for language-agnostic
  size               int  NOT NULL,
  distinct_reviewers int  NOT NULL,
  title              text NOT NULL,                 -- merged title, or the most central member's
  description        text NOT NULL,
  merged_from        int  NOT NULL DEFAULT 1,       -- clusters joined by the merge step
  PRIMARY KEY (run_id, cluster_hash)
);

CREATE TABLE cluster_members (
  run_id       bigint NOT NULL,
  cluster_hash text   NOT NULL,
  comment_id   bigint NOT NULL REFERENCES extractions(comment_id),
  PRIMARY KEY (run_id, comment_id),
  FOREIGN KEY (run_id, cluster_hash) REFERENCES clusters(run_id, cluster_hash) ON DELETE CASCADE
);

-- Merge decisions cached by the pair of cluster hashes, so reruns are
-- deterministic and cheap.
CREATE TABLE merge_decisions (
  pair_key           text PRIMARY KEY,              -- sha256 of the two sorted cluster hashes
  same               boolean NOT NULL,
  merged_title       text,
  merged_description text,
  model              text NOT NULL,
  decided_at         timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE merge_decisions;
DROP TABLE cluster_members;
DROP TABLE clusters;
DROP TABLE cluster_runs;
