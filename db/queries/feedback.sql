-- name: InsertFeedbackEvent :execrows
-- One signal per (finding, kind, actor): a repeated observation is a no-op.
INSERT INTO feedback_events (finding_id, kind, actor, role, weight, via, reason_text, teach_class, evidence)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (finding_id, kind, actor) DO NOTHING;

-- name: FeedbackEventsForFinding :many
SELECT * FROM feedback_events WHERE finding_id = $1 ORDER BY id;

-- name: FindingByComment :many
-- The production finding whose posted comment starts a review thread.
SELECT f.id, f.rule_id FROM findings f
JOIN rule_runs rr ON rr.id = f.rule_run_id
JOIN reviews r ON r.id = rr.review_id
WHERE f.repo = $1 AND f.pr_number = $2 AND f.gh_comment_id = $3 AND r.caller = 'production'
ORDER BY f.id DESC LIMIT 1;

-- name: RefreshRuleWeeklyStats :exec
REFRESH MATERIALIZED VIEW CONCURRENTLY rule_weekly_stats;

-- name: RefreshReviewDailyStats :exec
REFRESH MATERIALIZED VIEW CONCURRENTLY review_daily_stats;

-- name: FindingOutcome :one
SELECT * FROM finding_outcomes WHERE id = $1;

-- name: DemotionStats :many
-- Per rule, its last @last_n findings with a judgment (a
-- positive or negative weight in finding_outcomes). A finding is a
-- negative when its negative weight outweighs its positive weight.
-- non_author_actors counts distinct non-author people with an explicit
-- negative (wrong, not important, disagree; reactions are not explicit).
WITH judged AS (
  SELECT o.id, o.rule_id, o.neg_w, o.pos_w, o.owner_overturned,
         row_number() OVER (PARTITION BY o.rule_id ORDER BY o.created_at DESC, o.id DESC) AS n
  FROM finding_outcomes o WHERE o.pos_w > 0 OR o.neg_w > 0
), recent AS (
  SELECT * FROM judged WHERE n <= @last_n::int
)
SELECT r.rule_id,
       count(*)::int                                             AS judged,
       count(*) FILTER (WHERE r.neg_w > r.pos_w)::int            AS negatives,
       coalesce(sum(r.neg_w), 0)::float8                         AS neg_w,
       coalesce(sum(r.pos_w), 0)::float8                         AS pos_w,
       coalesce(bool_or(r.owner_overturned), false)::boolean     AS adjudicated,
       (SELECT count(DISTINCT e.actor) FROM effective_events e
        JOIN recent r2 ON r2.id = e.finding_id
        WHERE r2.rule_id = r.rule_id AND e.role <> 'author'
          AND e.kind IN ('fb_wrong', 'fb_not_important', 'fb_disagree'))::int AS non_author_actors
FROM recent r
GROUP BY r.rule_id
ORDER BY r.rule_id;

-- name: PRHadSecurityFindings :one
-- Whether a production review of this PR posted a Security finding, so an
-- upload with none on this head still closes its code-scanning alerts.
SELECT EXISTS (
  SELECT 1 FROM findings f
  JOIN rule_runs rr ON rr.id = f.rule_run_id
  JOIN reviews r ON r.id = rr.review_id AND r.caller = 'production'
  WHERE f.repo = $1 AND f.pr_number = $2 AND f.cc_label LIKE '%security)'
    AND f.state IN ('posted', 'resolved', 'outdated', 'minimized')
)::boolean AS had;

-- name: FindingsForAlert :many
-- The posted production findings a code-scanning alert of Exemplar's may
-- stand for: same PR, rule and file. The caller picks the one
-- at the alert's line, or the only one.
SELECT f.id, f.start_line FROM findings f
JOIN rule_runs rr ON rr.id = f.rule_run_id
JOIN reviews r ON r.id = rr.review_id AND r.caller = 'production'
WHERE f.repo = $1 AND f.pr_number = $2 AND f.rule_id = $3 AND f.path = $4
  AND f.cc_label LIKE '%security)' AND f.gh_comment_id IS NOT NULL
ORDER BY f.id DESC;

-- name: SetSarifAlert :exec
UPDATE findings SET sarif_alert_number = $2 WHERE id = $1;

-- name: FindingDetail :one
SELECT id, rule_id, lane, path, side, start_line, end_line, message, fingerprint FROM findings WHERE id = $1;

-- name: FindingsOnLines :many
-- Production findings of this PR on the same file and side that overlap
-- a later human comment's lines (the human raised it independently):
-- posted, shadow or suppressed, one per identity, made before the comment.
-- Exact duplicates, bad anchors, repeats of a posted finding and findings
-- already credited as human duplicates are left out.
SELECT DISTINCT ON (f.fingerprint) f.id, f.rule_id, f.lane, f.path, f.side, f.start_line, f.end_line, f.message, f.fingerprint
FROM findings f
JOIN rule_runs rr ON rr.id = f.rule_run_id
JOIN reviews r ON r.id = rr.review_id AND r.caller = 'production'
WHERE f.repo = @repo AND f.pr_number = @pr_number AND f.path = @path AND f.side = @side
  AND f.start_line <= @end_line::int AND f.end_line >= @start_line::int AND f.created_at < @before
  AND coalesce(f.suppressed_reason, '') NOT IN ('duplicate', 'bad_anchor', 'already_posted', 'human_duplicate')
ORDER BY f.fingerprint, f.id DESC
LIMIT 20;

-- name: FindingHasFeedback :one
SELECT EXISTS (SELECT 1 FROM feedback_events WHERE finding_id = $1)::boolean AS has;

-- name: FindingHasEventKind :one
SELECT EXISTS (SELECT 1 FROM feedback_events WHERE finding_id = $1 AND kind = $2)::boolean AS has;

-- name: PRsToPollReactions :many
-- PRs with Exemplar comments whose production reviews finished in the
-- last @since_hours hours (normally PRs updated in 24 h), with the
-- installation that last delivered an event for the repo.
SELECT DISTINCT r.repo, r.pr_number,
       (SELECT e.installation_id FROM gh_events e WHERE e.repo_full_name = r.repo ORDER BY e.received_at DESC LIMIT 1)::bigint AS installation_id
FROM reviews r
WHERE r.caller = 'production' AND r.finished_at > now() - make_interval(hours => @since_hours::int)
  AND EXISTS (SELECT 1 FROM rule_runs rr JOIN findings f ON f.rule_run_id = rr.id
              WHERE rr.review_id = r.id AND f.gh_comment_id IS NOT NULL)
  AND EXISTS (SELECT 1 FROM gh_events e WHERE e.repo_full_name = r.repo)
ORDER BY r.repo, r.pr_number;

-- name: CommentedFindings :many
-- A PR's production findings that were posted as comments.
SELECT f.id, f.rule_id, f.gh_comment_id::bigint AS gh_comment_id FROM findings f
JOIN rule_runs rr ON rr.id = f.rule_run_id
JOIN reviews r ON r.id = rr.review_id AND r.caller = 'production'
WHERE f.repo = $1 AND f.pr_number = $2 AND f.gh_comment_id IS NOT NULL;

-- name: RuleRecentOutcomes :one
-- Rule expiry: a rule's judged production findings over the
-- last @days days, with their weights.
SELECT count(*) FILTER (WHERE pos_w > 0 OR neg_w > 0)::int AS judged,
       coalesce(sum(neg_w), 0)::float8 AS neg_w, coalesce(sum(pos_w), 0)::float8 AS pos_w
FROM finding_outcomes
WHERE rule_id = @rule_id AND created_at > now() - make_interval(days => @days::int);

-- name: SetTeachClass :exec
-- Teach-the-bot: the class of an event's free text.
UPDATE feedback_events SET teach_class = $4 WHERE finding_id = $1 AND kind = $2 AND actor = $3;

-- name: UpsertVerifierLabel :exec
-- Routing by reason: a person's judgment of a finding is a
-- verifier calibration label. A human review (an owner's adjudication)
-- replaces an author's signal, never the reverse.
INSERT INTO verifier_labels (finding_id, label, source) VALUES ($1, $2, $3)
ON CONFLICT (finding_id) DO UPDATE SET label = excluded.label, source = excluded.source, labeled_at = now()
WHERE verifier_labels.source = 'author_signal' OR excluded.source = 'human_review';

-- name: TeachSignals :many
-- Teach-the-bot evidence over the last @days days: each
-- person's latest rejection of a finding with its class. A reason's class
-- wins; without one (or noise), "not important here" is a scope signal for
-- path clustering and "disagree" stays a disagreement.
SELECT f.rule_id, f.id AS finding_id, f.repo, f.pr_number, f.path, coalesce(f.gh_comment_id, 0)::bigint AS gh_comment_id, e.role,
       (CASE WHEN fe.teach_class IN ('scope', 'exception', 'rule_wrong') THEN fe.teach_class
             WHEN e.kind = 'fb_not_important' THEN 'scope'
             WHEN e.kind = 'fb_disagree' THEN 'disagree' END)::text AS class
FROM effective_events e
JOIN feedback_events fe ON fe.id = e.id
JOIN findings f ON f.id = e.finding_id
WHERE e.observed_at > now() - make_interval(days => @days::int) AND e.role <> 'observer'
  AND (fe.teach_class IN ('scope', 'exception', 'rule_wrong') OR e.kind IN ('fb_not_important', 'fb_disagree'))
ORDER BY f.rule_id, f.id;

-- name: InsertTeachProposal :execrows
INSERT INTO teach_proposals (rule_id, rule_version, teach_class, evidence_findings, config_pr_url, state)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (rule_id, rule_version, teach_class) DO NOTHING;

-- name: TeachProposalExists :one
SELECT EXISTS (SELECT 1 FROM teach_proposals WHERE rule_id = $1 AND rule_version = $2 AND teach_class = $3)::boolean AS has;

-- name: FindingRef :one
-- Where a finding lives, and the installation that last delivered an
-- event for its repo (feedback links carry only the finding id).
SELECT f.id, f.rule_id, f.repo, f.pr_number,
       coalesce((SELECT e.installation_id FROM gh_events e WHERE e.repo_full_name = f.repo ORDER BY e.received_at DESC LIMIT 1), 0)::bigint AS installation_id
FROM findings f WHERE f.id = $1;
