-- name: PutConfigSnapshot :exec
-- A version's tree never changes; loading it again only marks it current.
INSERT INTO config_snapshots (version, files) VALUES (@version, @files)
ON CONFLICT (version) DO UPDATE SET loaded_at = now();

-- name: GetConfigSnapshot :one
SELECT files FROM config_snapshots WHERE version = @version;

-- name: DeleteExpiredConfigSnapshots :execrows
-- The version in use is kept however old it is.
DELETE FROM config_snapshots WHERE version IN (
  SELECT c.version FROM config_snapshots c WHERE c.loaded_at < @cutoff AND c.version <> @keep LIMIT @batch
);

-- name: CurrentConfigSnapshot :one
-- The version the worker loaded last (loading marks it current).
SELECT version, files FROM config_snapshots ORDER BY loaded_at DESC LIMIT 1;
