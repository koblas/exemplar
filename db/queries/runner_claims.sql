-- name: PutRunnerClaim :exec
-- A retry's claim replaces the last attempt's, so its nonce stops working.
INSERT INTO runner_claims (job_id, attempt, nonce_hash, tokens, config_version, expires_at)
VALUES (@job_id, @attempt, @nonce_hash, @tokens, @config_version, @expires_at)
ON CONFLICT (job_id) DO UPDATE SET
  attempt = excluded.attempt, nonce_hash = excluded.nonce_hash, tokens = excluded.tokens,
  config_version = excluded.config_version, expires_at = excluded.expires_at,
  created_at = now(), claimed_at = NULL, released_at = NULL;

-- name: RedeemRunnerClaim :one
-- Once: a claimed, released or expired row, or another nonce, matches nothing.
UPDATE runner_claims SET claimed_at = now()
WHERE job_id = @job_id AND nonce_hash = @nonce_hash
  AND claimed_at IS NULL AND released_at IS NULL AND tokens IS NOT NULL AND expires_at > now()
RETURNING tokens, config_version;

-- name: ReleaseRunnerClaim :exec
UPDATE runner_claims SET tokens = NULL, released_at = coalesce(released_at, now()) WHERE job_id = @job_id;

-- name: ClearExpiredRunnerClaims :execrows
-- Tokens past their expiry are useless; finished rows go after a day.
WITH doomed AS (
  SELECT c.job_id FROM runner_claims c
  WHERE (c.tokens IS NOT NULL AND c.expires_at < now()) OR c.created_at < @cutoff
  LIMIT @batch
)
DELETE FROM runner_claims WHERE job_id IN (SELECT job_id FROM doomed);

-- name: InstalledRepos :many
-- Every repo with a webhook delivery, with its latest installation.
SELECT DISTINCT ON (repo_full_name) repo_full_name::text AS repo, installation_id::bigint AS installation_id
FROM gh_events WHERE repo_full_name IS NOT NULL AND installation_id IS NOT NULL
ORDER BY repo_full_name, received_at DESC;
