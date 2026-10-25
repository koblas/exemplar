// Package pipeline persists review results and posts them to GitHub. The
// production worker and replay share it, so a replayed review is written
// and posted exactly as a live one.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/feedbacklink"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/upstream"
)

// Record tracks the persisted rows for one review.
type Record struct {
	ReviewID int64
	// RunIDs maps a scoped rule id to its rule_runs.id.
	RunIDs map[string]int64
	repo   string
	pr     int
	caller string
	// feedback weighs the feedback events the review records.
	feedback *schema.FeedbackConfig
	// Links, when set, signs each comment's feedback links.
	Links *feedbacklink.Signer
	// LinkBase is the repository's web URL, for line links in a grouped
	// comment ("https://github.com/org/repo"); empty lists places as text.
	LinkBase string
}

// Begin creates (or, on retry, finds) the review row and its rule runs.
// attempt is reviews.attempt (0 means 1). With a nil store it returns an
// empty record and nothing is persisted.
func Begin(ctx context.Context, st *store.Store, p *engine.Prepared, caller string, attempt int, scoped []engine.ScopedRule) (*Record, error) {
	rec := &Record{RunIDs: map[string]int64{}, repo: p.PR.Repo, pr: p.PR.Number, caller: caller, feedback: p.Effective.Global.Feedback}
	if st == nil {
		return rec, nil
	}
	q := st.Queries
	for _, rule := range p.Effective.Rules {
		spec, err := json.Marshal(rule.Rule)
		if err != nil {
			return nil, fmt.Errorf("encode rule %s: %w", rule.ID, err)
		}
		var reviewBy pgtype.Date
		if t, err := time.Parse(time.DateOnly, rule.ReviewBy); err == nil {
			reviewBy = pgtype.Date{Time: t, Valid: true}
		}
		if err := q.UpsertRule(ctx, queries.UpsertRuleParams{
			RuleID: rule.ID, VersionSha: rule.VersionSHA, Scope: rule.Scope, Kind: rule.Kind,
			Category: rule.Category, Severity: rule.Severity, Status: rule.Status, Owner: rule.Owner,
			FixPolicy: rule.FixPolicy, ReviewBy: reviewBy, Exported: rule.Export != nil && rule.Export.Agents,
			ModelTier: rule.ModelTier, Spec: spec,
		}); err != nil {
			return nil, fmt.Errorf("mirror rule %s: %w", rule.ID, err)
		}
	}
	rv, err := q.CreateReview(ctx, queries.CreateReviewParams{
		Repo: p.PR.Repo, PrNumber: int32(p.PR.Number), HeadSha: p.PR.HeadSHA, BaseSha: p.PR.BaseSHA,
		ConfigVersion: p.Effective.ConfigVersion, Caller: caller, Attempt: int32(max(attempt, 1)),
	})
	if err != nil {
		return nil, fmt.Errorf("create review: %w", err)
	}
	if err := q.StartReview(ctx, rv.ID); err != nil {
		return nil, fmt.Errorf("start review: %w", err)
	}
	// Only the agent's name is kept, for upstream effectiveness;
	// never the commit messages or labels it was read from.
	if caller == "production" {
		agent := upstream.Detect(p.PR.Author, p.PR.Labels, p.PR.CommitMessages)
		if err := q.SetReviewAgent(ctx, queries.SetReviewAgentParams{ID: rv.ID, Agent: &agent, AgentReads: nonNil(upstream.Reads(agent))}); err != nil {
			return nil, fmt.Errorf("record review agent: %w", err)
		}
	}
	rec.ReviewID = rv.ID
	for _, s := range scoped {
		run, err := q.UpsertRuleRun(ctx, queries.UpsertRuleRunParams{
			ReviewID: rv.ID, RuleID: s.Rule.ID, RuleVersion: s.Rule.VersionSHA, Lane: s.Lane, ScopedFiles: s.Files,
		})
		if err != nil {
			return nil, fmt.Errorf("create rule run %s: %w", s.Rule.ID, err)
		}
		rec.RunIDs[s.Rule.ID] = run.ID
	}
	return rec, nil
}

// Finish writes rule runs, findings, the review outcome and the gate trace.
func Finish(ctx context.Context, st *store.Store, rec *Record, res *engine.Result) error {
	if st == nil {
		return nil
	}
	q := st.Queries
	runIDs := make([]int64, len(res.Runs))
	for i, run := range res.Runs {
		if run.Lane == "verifier" || run.Lane == engine.LaneCategorizer {
			continue // verifier and categorizer calls are accounted in model_calls, not as rules
		}
		row, err := q.UpsertRuleRun(ctx, queries.UpsertRuleRunParams{
			ReviewID: rec.ReviewID, RuleID: run.RuleID, RuleVersion: run.RuleVersion, Lane: run.Lane, ScopedFiles: nonNil(run.ScopedFiles),
		})
		if err != nil {
			return fmt.Errorf("rule run %s: %w", run.RuleID, err)
		}
		runIDs[i] = row.ID
		cov := float32(run.Coverage)
		in, cached, out := int32(run.Usage.Input), int32(run.Usage.CacheRead), int32(run.Usage.Output)
		latency := int32(run.LatencyMS)
		cost := run.CostUSD
		var carriedFrom *int64
		if run.Carried {
			carriedFrom = &run.CarriedFrom
		}
		if err := q.FinishRuleRun(ctx, queries.FinishRuleRunParams{
			ID: row.ID, Status: run.Status, Coverage: &cov, ContextDegraded: run.ContextDegraded, Model: strPtr(run.Model),
			InputTokens: &in, CachedTokens: &cached, OutputTokens: &out, CostUsd: &cost, LatencyMs: &latency,
			CarriedFrom: carriedFrom,
		}); err != nil {
			return fmt.Errorf("finish rule run %s: %w", run.RuleID, err)
		}
	}
	var total float64
	for _, run := range res.Runs {
		total += run.CostUSD
	}
	for i := range res.Findings {
		f := &res.Findings[i]
		runID := runIDs[findingRun(res, f)]
		if runID == 0 {
			continue
		}
		id, err := q.InsertFinding(ctx, queries.InsertFindingParams{
			RuleRunID: runID, Repo: rec.repo, PrNumber: int32(rec.pr), RuleID: f.RuleID, Lane: f.Lane,
			Severity: f.Severity, CcLabel: f.CCLabel, Path: f.Anchor.Path, Side: string(f.Anchor.Side),
			StartLine: int32(f.Anchor.StartLine), EndLine: int32(f.Anchor.EndLine), Fingerprint: f.Fingerprint,
			Message: f.Message, VerifyVerdict: strPtr(f.VerifyVerdict), VerifyImportance: f32(f.VerifyImportance),
			VerifyModel: strPtr(f.VerifyModel), VerifySameFamily: f.VerifySameFamily, VerifyEvidence: f.VerifyEvidenceRaw,
			SuggestedFix: strPtr(f.Fix), FixValid: f.FixValid, State: f.State, SuppressedReason: strPtr(f.Suppressed),
			AnchorCode: strPtr(f.AnchorCode), AnchorOrdinal: ordinal(f), GroupKey: strPtr(f.GroupKey),
			VerifyQuorumN: quorumN(f.VerifyQuorumN), VerifyQuorumAgreed: quorumAgreed(f),
		})
		if err != nil {
			return fmt.Errorf("finding: %w", err)
		}
		f.ID, f.RuleRunID = id, runID
	}
	// A withheld human duplicate is the reviewer's agreement.
	if _, err := RecordEvents(ctx, st, rec.feedback, humanAgreedEvents(res)); err != nil {
		return err
	}
	status := "done"
	if res.Incomplete != "" {
		status = "incomplete"
	}
	cov := float32(res.Coverage)
	verdict := res.Verdict
	if err := q.FinishReview(ctx, queries.FinishReviewParams{
		ID: rec.ReviewID, Status: status, Coverage: &cov, Degraded: res.Degraded,
		IncompleteReason: strPtr(res.Incomplete), Verdict: &verdict, CostUsd: total,
		BugLane: res.BugLane != nil && res.BugLane.Run,
	}); err != nil {
		return fmt.Errorf("finish review: %w", err)
	}
	stage1, err := json.Marshal(res.Stage1)
	if err != nil {
		return fmt.Errorf("encode stage 1 gate: %w", err)
	}
	var stage2 []byte
	if res.Stage2 != nil {
		if stage2, err = json.Marshal(res.Stage2); err != nil {
			return fmt.Errorf("encode stage 2 gate: %w", err)
		}
	}
	if err := q.UpsertApprovalDecision(ctx, queries.UpsertApprovalDecisionParams{
		ReviewID: rec.ReviewID, Stage1: stage1, Stage2: stage2, Decision: res.Decision(),
	}); err != nil {
		return fmt.Errorf("record approval decision: %w", err)
	}
	return nil
}

// findingRun returns the index of the run a finding came from.
func findingRun(res *engine.Result, f *engine.Finding) int {
	for i, r := range res.Runs {
		if r.RuleID == f.RuleID && r.Lane == f.Lane {
			return i
		}
	}
	return 0
}

// Post submits res as one COMMENT review and completes its check run;
// checkID is a check run already started for the review (0 starts one).
// Posted findings are marked posted in the store.
func Post(ctx context.Context, st *store.Store, poster *gh.Poster, p *engine.Prepared, rec *Record, res *engine.Result, checkID int64) (*PostOutcome, error) {
	pr, err := gh.ParsePR(p.PR.Repo, p.PR.Number, p.PR.HeadSHA)
	if err != nil {
		return nil, fmt.Errorf("parse pr: %w", err)
	}
	if checkID == 0 {
		if checkID, err = poster.StartCheck(ctx, pr, CheckExternalID(rec.ReviewID)); err != nil {
			return nil, fmt.Errorf("start check run: %w", err)
		}
	}
	var comments []gh.Comment
	for _, f := range res.Findings {
		if f.State != engine.StatePosted || f.GroupMember() {
			continue
		}
		comments = append(comments, gh.Comment{
			Path: f.Anchor.Path, Side: string(f.Anchor.Side), StartLine: f.Anchor.StartLine, EndLine: f.Anchor.EndLine,
			Body: commentBody(f, rec, p.PR.HeadSHA),
		})
	}
	summary := checkSummary(p, res)
	ghID, isNew, err := poster.PostReview(ctx, pr, gh.ReviewPost{ReviewID: rec.ReviewID, Summary: summary, Comments: comments})
	if err != nil {
		return nil, fmt.Errorf("post review: %w", err)
	}
	// The check run leads with the verdict: who reviews this PR. The
	// conclusion stays neutral; the verdict informs, it never fails the PR.
	v := verdict(res)
	if err := poster.CompleteCheck(ctx, pr, checkID, gh.CheckResult{
		Conclusion: "neutral", Title: fmt.Sprintf("%d finding(s) · %s", len(comments), v.Title()),
		Summary: v.Line() + "\n\n" + summary + scopeNote(p, res), Text: checkText(p, res),
	}); err != nil {
		return nil, fmt.Errorf("complete check run: %w", err)
	}
	if st != nil && len(comments) > 0 {
		if err := mapComments(ctx, st, poster, pr, ghID, rec, res); err != nil {
			return nil, err
		}
	}
	return &PostOutcome{GitHubReviewID: ghID, NewPost: isNew, Comments: len(comments), CheckRunID: checkID}, nil
}

// mapComments records each posted finding's comment id, so a later
// review can reply to it and resolve its thread. Comments are matched by
// path, side, end line and body, in order among equal ones.
func mapComments(ctx context.Context, st *store.Store, poster *gh.Poster, pr gh.PR, ghReviewID int64, rec *Record, res *engine.Result) error {
	posted, err := poster.ReviewComments(ctx, pr, ghReviewID)
	if err != nil {
		return fmt.Errorf("list review comments: %w", err)
	}
	key := func(path, side string, line int, body string) string {
		return fmt.Sprintf("%s\x00%s\x00%d\x00%s", path, side, line, body)
	}
	ids := map[string][]int64{}
	for _, c := range posted {
		k := key(c.Path, c.Side, c.Line, c.Body)
		ids[k] = append(ids[k], c.ID)
	}
	for _, f := range res.Findings {
		if f.State != engine.StatePosted || f.ID == 0 || f.GroupMember() {
			continue
		}
		var commentID *int64
		k := key(f.Anchor.Path, string(f.Anchor.Side), f.Anchor.EndLine, commentBody(f, rec, pr.HeadSHA))
		if q := ids[k]; len(q) > 0 {
			commentID, ids[k] = &q[0], q[1:]
		}
		if err := st.MarkFindingPosted(ctx, queries.MarkFindingPostedParams{ID: f.ID, GhCommentID: commentID}); err != nil {
			return fmt.Errorf("mark finding %d posted: %w", f.ID, err)
		}
		// A group's members share the leader's comment and thread (FR-C9).
		if f.GroupKey != "" && commentID != nil {
			if err := st.ShareGroupComment(ctx, queries.ShareGroupCommentParams{
				GhCommentID: commentID, Repo: rec.repo, PrNumber: int32(rec.pr), GroupKey: &f.GroupKey,
			}); err != nil {
				return fmt.Errorf("share group comment of finding %d: %w", f.ID, err)
			}
		}
	}
	return nil
}

func countCarried(res *engine.Result) int {
	n := 0
	for _, r := range res.Runs {
		if r.Carried {
			n++
		}
	}
	return n
}

// CheckExternalID is a review's check-run external id.
func CheckExternalID(reviewID int64) string { return fmt.Sprintf("review-%d", reviewID) }

// PostOutcome records what was posted to GitHub.
type PostOutcome struct {
	GitHubReviewID int64 `json:"github_review_id"`
	NewPost        bool  `json:"new_post"`
	Comments       int   `json:"comments"`
	CheckRunID     int64 `json:"check_run_id"`
}

var (
	// fenceRunRE is three or more backticks or tildes: a code fence, which
	// in a message could open a suggestion block.
	fenceRunRE = regexp.MustCompile("`{3,}|~{3,}")
	// tagRE is the start of an HTML tag or comment.
	tagRE = regexp.MustCompile(`<([A-Za-z/!])`)
)

// zeroWidth (U+200B) breaks markup GitHub would otherwise parse.
const zeroWidth = "\u200b"

// postableMessage makes a model-written message safe to post as prose:
// it cannot open a code fence (a suggestion block would skip fix
// validation), mention people or teams, or carry HTML such as a forged
// rule footer. Short inline code stays.
func postableMessage(msg string) string {
	msg = fenceRunRE.ReplaceAllStringFunc(msg, func(run string) string {
		return strings.Join(strings.Split(run, ""), zeroWidth)
	})
	msg = tagRE.ReplaceAllString(msg, "<"+zeroWidth+"$1")
	return strings.ReplaceAll(msg, "@", "@"+zeroWidth)
}

func commentBody(f engine.Finding, rec *Record, head string) string {
	links := rec.Links
	var b strings.Builder
	fmt.Fprintf(&b, "**%s:** %s", f.CCLabel, postableMessage(f.Message))
	if len(f.Members) > 0 {
		// One comment for every occurrence of the rule (FR-C9); one fix
		// cannot cover them all, so no suggestion.
		fmt.Fprintf(&b, "\n\nAlso in %d other place(s); a reply here applies to all of them:\n", len(f.Members))
		for _, a := range f.Members {
			fmt.Fprintf(&b, "\n- %s", placeLink(rec.LinkBase, head, a.Path, a.StartLine, a.EndLine))
		}
	} else if f.FixValid != nil && *f.FixValid {
		// Replaces exactly the anchored lines.
		b.WriteString("\n\n```suggestion\n" + engine.SuggestionText(f) + "\n```")
	}
	if links != nil && f.ID != 0 {
		b.WriteString("\n\n" + links.Footer(f.ID))
	}
	b.WriteString("\n\n<sub>Rule `" + f.RuleID + "`")
	if f.Citation != "" {
		b.WriteString(" · [why](" + f.Citation + ")")
	}
	// Every comment offers structured responses.
	if links != nil && f.ID != 0 {
		b.WriteString(" · or reply `/exemplar <reason>`</sub>")
	} else {
		b.WriteString(" · reply `/exemplar fixed`, `wrong`, `not-important`, `handled` or `disagree`</sub>")
	}
	return b.String()
}

func checkSummary(p *engine.Prepared, res *engine.Result) string {
	posted, open := 0, 0
	for _, f := range res.Findings {
		switch {
		case f.State == engine.StatePosted:
			posted++
		case f.Suppressed == engine.SuppressAlreadyPosted:
			open++
		}
	}
	s := checkSummaryNew(p, res, posted)
	if open > 0 {
		s += fmt.Sprintf(" %d finding(s) from earlier pushes still apply.", open)
	}
	if res.Summarized > 0 {
		s += fmt.Sprintf(" The round cap is reached: %d new finding(s) are listed in the round summary comment instead of inline.", res.Summarized)
	}
	return s
}

func checkSummaryNew(p *engine.Prepared, res *engine.Result, posted int) string {
	if posted == 0 && res.Summarized > 0 {
		return "Nothing posted inline."
	}
	if posted == 0 {
		if res.Coverage >= 1 {
			return "No findings."
		}
		return fmt.Sprintf("No findings from the rules that ran (coverage %.0f%%).", res.Coverage*100)
	}
	s := fmt.Sprintf("Exemplar reviewed %d file(s) and posted %d finding(s) (coverage %.0f%%).", len(p.Files), posted, res.Coverage*100)
	if res.HeldBack > 0 {
		s += fmt.Sprintf(" %d lower-priority finding(s) held back by the comment budget.", res.HeldBack)
	}
	return s
}

// scopeNote states, on an install without exemplar-search, what the review
// could not look at: callers in other repositories, naming the repo's
// context.extra_repos when it has them.
func scopeNote(p *engine.Prepared, res *engine.Result) string {
	if !res.SearchOff {
		return ""
	}
	s := "\n\nCross-repository callers were not checked (search is not configured)."
	if p.Effective != nil && p.Effective.RepoConfig.Context != nil && len(p.Effective.RepoConfig.Context.ExtraRepos) > 0 {
		s += " Not searched: " + strings.Join(p.Effective.RepoConfig.Context.ExtraRepos, ", ") + "."
	}
	return s
}

func checkText(p *engine.Prepared, res *engine.Result) string {
	var b strings.Builder
	if carried := countCarried(res); carried > 0 {
		fmt.Fprintf(&b, "- %d rule(s) carried forward from %s: none of their files changed since that review.\n", carried, res.CarriedFrom[:min(7, len(res.CarriedFrom))])
	}
	for _, run := range res.Runs {
		if run.Status != engine.RunOK && run.Lane != "verifier" {
			fmt.Fprintf(&b, "- Rule `%s` did not complete: %s %s\n", run.RuleID, run.Status, run.Detail)
		}
	}
	for _, x := range p.Excluded {
		fmt.Fprintf(&b, "- Not reviewed: `%s` (%s)\n", x.Path, x.Reason)
	}
	if res.Summarized > 0 {
		fmt.Fprintf(&b, "- %d finding(s) held back by the round cap (`round_cap`), listed in the round summary comment.\n", res.Summarized)
	}
	if res.Grouped > 0 {
		fmt.Fprintf(&b, "- %d more occurrence(s) of a rule are listed in that rule's grouped comment instead of their own.\n", res.Grouped)
	}
	failed, shed := 0, 0
	for _, f := range res.Findings {
		switch f.Suppressed {
		case engine.SuppressQuorum:
			failed++
		case engine.SuppressQuorumShed:
			shed++
		}
	}
	if failed > 0 {
		fmt.Fprintf(&b, "- %d finding(s) held back: fewer than the verifier quorum confirmed them (`quorum`).\n", failed)
	}
	if shed > 0 {
		fmt.Fprintf(&b, "- %d finding(s) held back: the verifier quorum was shed at the run's soft cost ceiling (`quorum_shed`).\n", shed)
	}
	b.WriteString("\nCost and duration are informational.\n")
	return b.String()
}

// ordinal is the finding's k, when it has anchor code to go with it.
func ordinal(f *engine.Finding) *int32 {
	if f.AnchorCode == "" {
		return nil
	}
	k := int32(f.Ordinal)
	return &k
}

// quorumN is a quorum candidate's verifier count; NULL for findings one
// verifier checked.
func quorumN(n int) *int32 {
	if n == 0 {
		return nil
	}
	v := int32(min(n, 1<<30))
	return &v
}

// quorumAgreed is how many of a quorum candidate's verifiers confirmed it.
func quorumAgreed(f *engine.Finding) *int32 {
	if f.VerifyQuorumN == 0 {
		return nil
	}
	v := int32(min(f.VerifyQuorumAgreed, 1<<30))
	return &v
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func f32(v *float64) *float32 {
	if v == nil {
		return nil
	}
	f := float32(*v)
	return &f
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// placeLink is path:lines, linked to the head when base is set.
func placeLink(base, head, path string, start, end int) string {
	lines, anchor := strconv.Itoa(start), "L"+strconv.Itoa(start)
	if end > start {
		lines += "-" + strconv.Itoa(end)
		anchor += "-L" + strconv.Itoa(end)
	}
	if base == "" {
		return "`" + path + ":" + lines + "`"
	}
	return fmt.Sprintf("[%s:%s](%s/blob/%s/%s#%s)", path, lines, base, head, path, anchor)
}
