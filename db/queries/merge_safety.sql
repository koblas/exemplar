-- name: RecentPathEvents :many
-- Which of paths had an incident or a revert since the given time, and
-- the latest of each (spec §6.13; the bug lane's 90-day trigger, §6.7).
SELECT path, kind, max(at)::timestamptz AS latest FROM path_events
WHERE repo = @repo AND path = ANY(@paths::text[]) AND at >= @since
GROUP BY path, kind;

-- name: PathChurn :many
-- Merged PRs per path since the given time, not counting the PR under
-- review. Reads mined_prs, so it is as fresh as the last ingest.
SELECT f.path::text AS path, count(*)::int AS prs
FROM mined_prs m, unnest(m.files) AS f(path)
WHERE m.repo = @repo AND m.merged_at >= @since AND m.pr_number <> @pr_number
  AND f.path = ANY(@paths::text[])
GROUP BY f.path;
