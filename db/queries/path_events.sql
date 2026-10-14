-- name: GetRevertsSince :one
SELECT reverts_since FROM path_event_cursors WHERE repo = $1;

-- name: SaveRevertsSince :exec
INSERT INTO path_event_cursors (repo, reverts_since) VALUES ($1, $2)
ON CONFLICT (repo) DO UPDATE SET reverts_since = GREATEST(path_event_cursors.reverts_since, EXCLUDED.reverts_since),
                                 updated_at = now();

-- name: InsertPathEvent :exec
INSERT INTO path_events (repo, path, kind, ref, at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING;

-- name: MinedPRFiles :one
SELECT files FROM mined_prs WHERE repo = $1 AND pr_number = $2;

-- name: UpsertRevertSeed :exec
-- A revert seed (spec §8.1): a reverted PR's unreviewed hunks in one file,
-- with the revert reason as the body. Re-seeding leaves the row alone.
INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at, comment_url,
                            body, diff_hunk, doc_urls, is_thread_root, thread_resolved, source,
                            commit_sha, start_line, end_line)
VALUES (@id, @repo, @pr_number, @path, @ext, @reviewer_pid, @author_pid, @created_at, @comment_url,
        @body::text, @diff_hunk::text, @doc_urls, true, false, 'revert_seed', @commit_sha, @start_line, @end_line)
ON CONFLICT (id) DO NOTHING;

-- name: PathEventsForPath :many
-- A path's incidents and reverts, newest first (path_history, spec §6.4).
SELECT kind, ref, at FROM path_events
WHERE repo = @repo AND path = @path
ORDER BY at DESC, kind, ref
LIMIT @max_rows;

-- name: InsertPRLink :exec
INSERT INTO pr_links (repo, pr_number, kind, ref, at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING;

-- name: UpsertPRMerge :exec
INSERT INTO pr_merges (repo, pr_number, head_sha, merged_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (repo, pr_number) DO UPDATE SET head_sha = excluded.head_sha, merged_at = excluded.merged_at;

-- name: CounterMetrics :many
-- Merged PRs over the last @days days, auto-approved against the rest.
SELECT auto_approved, count(*)::int AS merged, count(*) FILTER (WHERE reverted)::int AS reverted,
  count(*) FILTER (WHERE incident)::int AS incident, count(*) FILTER (WHERE reverted OR incident)::int AS failed
FROM merge_outcomes WHERE merged_at > now() - make_interval(days => @days::int)
GROUP BY auto_approved ORDER BY auto_approved;
