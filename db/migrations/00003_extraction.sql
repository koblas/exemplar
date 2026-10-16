-- Learning plane: mined comments and their extractions.
-- Ingest (needs GitHub access) fills mined_comments; the extraction
-- driver moves each comment pending -> in_batch -> accepted | rejected, or
-- error after repeated failures. Nothing is dropped silently.

-- +goose Up
CREATE TABLE mined_comments (
  id               bigint PRIMARY KEY,               -- GitHub comment id
  repo             text NOT NULL,
  pr_number        int  NOT NULL,
  path             text NOT NULL,
  ext              text NOT NULL,
  reviewer_pid     text NOT NULL,                     -- HMAC pseudonyms
  author_pid       text NOT NULL,
  created_at       timestamptz NOT NULL,
  comment_url      text NOT NULL,
  body             text,                              -- purged when uncited
  diff_hunk        text,
  doc_urls         text[] NOT NULL DEFAULT '{}',
  input_hash       text,                              -- extraction cache key
  is_thread_root   boolean NOT NULL DEFAULT true,
  thread_resolved  boolean NOT NULL DEFAULT false,
  source           text NOT NULL DEFAULT 'human' CHECK (source IN ('human', 'accepted_bot_finding', 'revert_seed')),
  extract_status   text NOT NULL DEFAULT 'pending' CHECK (extract_status IN ('pending', 'in_batch', 'accepted', 'rejected', 'error')),
  extract_batch_id text,
  extract_attempts int NOT NULL DEFAULT 0,
  extract_error    text
);
CREATE INDEX mined_comments_status_idx ON mined_comments (extract_status, id);
CREATE INDEX mined_comments_batch_idx ON mined_comments (extract_batch_id);

CREATE TABLE extractions (
  comment_id         bigint PRIMARY KEY REFERENCES mined_comments(id),
  title              text NOT NULL,
  description        text NOT NULL,
  preferred_approach text,
  applies_to_hint    text,
  category           text NOT NULL,
  language_agnostic  boolean NOT NULL DEFAULT false,
  confidence         real NOT NULL,
  extractor_model    text NOT NULL,
  extractor_version  text NOT NULL,
  embedding          halfvec(1024)                    -- filled by the embed job
);
CREATE INDEX extractions_embedding_idx ON extractions USING hnsw (embedding halfvec_cosine_ops);

-- Result cache keyed by input_hash = sha256(extractor_version | model | body | diff_hunk):
-- the same input never costs a second model call.
CREATE TABLE extraction_cache (
  input_hash        text PRIMARY KEY,
  extractor_version text NOT NULL,
  model             text NOT NULL,
  result            jsonb NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now()
);

-- One row per submitted Message Batch, so the driver can resume after a crash.
CREATE TABLE extraction_batches (
  batch_id          text PRIMARY KEY,
  extractor_version text NOT NULL,
  model             text NOT NULL,
  comment_count     int  NOT NULL,
  status            text NOT NULL DEFAULT 'submitted' CHECK (status IN ('submitted', 'processed', 'failed')),
  submitted_at      timestamptz NOT NULL DEFAULT now(),
  processed_at      timestamptz,
  accepted          int,
  rejected          int,
  returned          int                                  -- comments sent back to pending
);

-- +goose Down
DROP TABLE extraction_batches;
DROP TABLE extraction_cache;
DROP TABLE extractions;
DROP TABLE mined_comments;
