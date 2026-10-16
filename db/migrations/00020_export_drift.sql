-- Export drift: how far each repo's generated instruction
-- files are behind the config. export_runs records each config SHA the
-- export job first saw, so "versions behind" counts the config versions
-- exported since a file last matched its rendering.

-- +goose Up
CREATE TABLE export_runs (
  config_sha text PRIMARY KEY,
  first_seen timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE exports
  ADD COLUMN created_at  timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN current_sha text NOT NULL DEFAULT '',  -- the config the default branch's block last matched
  ADD COLUMN current_at  timestamptz;

-- One row per file: a current file is 0 behind; any other counts the
-- config versions since it was last current, or, never merged, since it
-- was first proposed (that version included).
CREATE VIEW export_file_drift AS
SELECT e.repo, e.path, e.target, e.state, e.config_sha, e.current_sha, e.pr_url, e.updated_at,
  CASE WHEN e.state = 'current' THEN 0
       WHEN e.current_at IS NOT NULL THEN (SELECT count(*) FROM export_runs r WHERE r.first_seen > e.current_at)
       ELSE 1 + (SELECT count(*) FROM export_runs r WHERE r.first_seen > e.created_at)
  END::int AS versions_behind
FROM exports e;

-- One row per repo: its furthest-behind file.
CREATE VIEW export_drift AS
SELECT repo,
  max(versions_behind)::int AS versions_behind,
  count(*)::int AS files,
  count(*) FILTER (WHERE state = 'proposed')::int AS proposed,
  count(*) FILTER (WHERE state = 'edited')::int AS edited,
  max(pr_url) FILTER (WHERE state = 'proposed') AS pr_url,
  min(updated_at) FILTER (WHERE state <> 'current') AS oldest_change
FROM export_file_drift
GROUP BY repo;

-- +goose Down
DROP VIEW export_drift;
DROP VIEW export_file_drift;
ALTER TABLE exports DROP COLUMN current_at, DROP COLUMN current_sha, DROP COLUMN created_at;
DROP TABLE export_runs;
