-- name: GetIngestCursor :one
SELECT cursor FROM ingest_cursors WHERE repo = $1;

-- name: SaveIngestCursor :exec
INSERT INTO ingest_cursors (repo, cursor, prs_seen, kept, filtered)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (repo) DO UPDATE SET
  cursor = EXCLUDED.cursor, updated_at = now(),
  prs_seen = ingest_cursors.prs_seen + EXCLUDED.prs_seen,
  kept = ingest_cursors.kept + EXCLUDED.kept,
  filtered = ingest_cursors.filtered + EXCLUDED.filtered;

-- name: UpsertIngestedComment :exec
-- A comment is stored once; re-ingesting it only refreshes whether its
-- thread was resolved. Bodies are never rewritten (they may be purged).
INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at, comment_url,
                            body, diff_hunk, doc_urls, is_thread_root, thread_resolved, source,
                            commit_sha, start_line, end_line, author_trusted)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, true, $13, 'human', $14, $15, $16, true)
ON CONFLICT (id) DO UPDATE SET thread_resolved = EXCLUDED.thread_resolved, author_trusted = true;

-- name: UpsertMinedPR :exec
INSERT INTO mined_prs (repo, pr_number, head_sha, base_sha, merged_at, approved_by_codeowner, files)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (repo, pr_number) DO UPDATE SET
  head_sha = EXCLUDED.head_sha, base_sha = EXCLUDED.base_sha, merged_at = EXCLUDED.merged_at,
  approved_by_codeowner = EXCLUDED.approved_by_codeowner, files = EXCLUDED.files;

-- name: UpsertPathOwner :exec
INSERT INTO path_owners (repo, path, owner) VALUES ($1, $2, $3)
ON CONFLICT (repo, path) DO UPDATE SET owner = EXCLUDED.owner;

-- name: StartAuthorRecheck :exec
-- Re-page a repo from the start; comments the pass does not keep are then
-- marked untrusted (FinishAuthorRecheck).
INSERT INTO ingest_cursors (repo, cursor, recheck_since) VALUES ($1, NULL, now())
ON CONFLICT (repo) DO UPDATE SET cursor = NULL, updated_at = now(),
  recheck_since = coalesce(ingest_cursors.recheck_since, now());

-- name: AuthorRecheckPending :one
SELECT (recheck_since IS NOT NULL)::bool FROM ingest_cursors WHERE repo = $1;

-- name: FinishAuthorRecheck :execrows
-- After a full pass: the repo's human comments it did not keep again.
UPDATE mined_comments SET author_trusted = false
WHERE repo = $1 AND source = 'human' AND author_trusted IS NULL;

-- name: ClearAuthorRecheck :exec
UPDATE ingest_cursors SET recheck_since = NULL WHERE repo = $1;
