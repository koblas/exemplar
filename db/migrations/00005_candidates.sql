-- Ranking output (spec §8.5): candidates built from the latest cluster run.
-- Candidates persist across cluster runs: each run's clusters are matched to
-- existing candidates by overlap of their source comments.

-- +goose Up

-- Incidents and reverts by path (spec §4.3, §8.1), filled by ingest; also
-- backs path_history and merge_safety_score later.
CREATE TABLE path_events (
  repo text NOT NULL,
  path text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('incident', 'revert')),
  ref  text NOT NULL,
  at   timestamptz NOT NULL,
  PRIMARY KEY (repo, path, kind, ref)
);

-- CODEOWNERS resolution per mined path, filled by ingest (which reads
-- CODEOWNERS from the default branch); rank takes the majority owner.
CREATE TABLE path_owners (
  repo  text NOT NULL,
  path  text NOT NULL,
  owner text NOT NULL,  -- first owner of the last matching CODEOWNERS rule
  PRIMARY KEY (repo, path)
);

CREATE TABLE candidates (
  id                 bigserial PRIMARY KEY,
  ext                text NOT NULL,          -- cluster partition: an extension, or '*' for language-agnostic
  title              text NOT NULL,
  description        text NOT NULL,
  cluster_size       int  NOT NULL,
  distinct_reviewers int  NOT NULL,
  score              real NOT NULL,
  score_detail       jsonb NOT NULL,         -- each factor of the score, for the candidate PR and audits
  owner              text NOT NULL,
  status             text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'pr_open', 'promoted', 'killed', 'retired')),
  config_pr_url      text,
  kill_reason        text,
  cluster_hash       text NOT NULL,          -- the cluster it was last matched to
  last_run_id        bigint NOT NULL REFERENCES cluster_runs(id),
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX candidates_rank_idx ON candidates (status, score DESC);

CREATE TABLE candidate_sources (
  candidate_id bigint NOT NULL REFERENCES candidates(id) ON DELETE CASCADE,
  comment_id   bigint NOT NULL REFERENCES mined_comments(id),
  PRIMARY KEY (candidate_id, comment_id)
);
CREATE INDEX candidate_sources_comment_idx ON candidate_sources (comment_id);

-- +goose Down
DROP TABLE candidate_sources;
DROP TABLE candidates;
DROP TABLE path_owners;
DROP TABLE path_events;
