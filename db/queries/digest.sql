-- Owner digests (FR-B5, PRD §17.8): the same views the dashboards and
-- auto-demotion read.

-- name: DigestRuleStats :many
SELECT rule_id,
       count(*) FILTER (WHERE seen)::int AS posted,
       count(*) FILTER (WHERE seen AND accepted)::int AS accepted,
       count(*) FILTER (WHERE seen AND (accepted OR rejected OR ignored))::int AS judged,
       coalesce(sum(neg_w), 0)::float8 AS neg_w,
       coalesce(sum(pos_w), 0)::float8 AS pos_w
FROM finding_outcomes
WHERE created_at > now() - make_interval(days => @days::int)
GROUP BY rule_id;

-- name: DigestDismissals :many
-- Each actor's latest negative reason, by rule and path, in the window.
SELECT f.rule_id, e.kind, f.path, count(*)::int AS n
FROM effective_events e JOIN findings f ON f.id = e.finding_id
WHERE e.kind IN ('fb_wrong', 'fb_not_important', 'fb_handled', 'fb_disagree')
  AND e.observed_at > now() - make_interval(days => @days::int)
GROUP BY f.rule_id, e.kind, f.path;

-- name: DigestAdjudicationQueue :many
-- Findings an author marked wrong or disagreed with (their latest signal)
-- and no owner has adjudicated (spec §9.4).
SELECT f.id, f.rule_id, f.repo, f.pr_number, coalesce(f.gh_comment_id, 0)::bigint AS gh_comment_id, e.kind, e.observed_at
FROM effective_events e JOIN findings f ON f.id = e.finding_id
WHERE e.role = 'author' AND e.kind IN ('fb_wrong', 'fb_disagree')
  AND NOT EXISTS (SELECT 1 FROM feedback_events a WHERE a.finding_id = e.finding_id AND a.kind LIKE 'adjudicated\_%')
ORDER BY e.observed_at;

-- name: DigestAutoApproved :many
-- PRs auto-approved in the window whose touched paths a team owns in
-- CODEOWNERS (FR-A5), from the Stage 2 trace.
SELECT r.repo, r.pr_number, left(r.head_sha, 12)::text AS head,
       (SELECT string_agg(DISTINCT p.key, ', ') FROM jsonb_each(d.stage2->'codeowners'->'owners') p, jsonb_array_elements_text(p.value) o
        WHERE lower(o) = lower(@owner::text))::text AS paths
FROM approval_decisions d JOIN reviews r ON r.id = d.review_id
WHERE d.decision = 'approved' AND r.caller = 'production'
  AND coalesce(r.finished_at, r.started_at) > now() - make_interval(days => @days::int)
  AND EXISTS (SELECT 1 FROM jsonb_each(d.stage2->'codeowners'->'owners') p, jsonb_array_elements_text(p.value) o WHERE lower(o) = lower(@owner::text))
ORDER BY r.repo, r.pr_number;
