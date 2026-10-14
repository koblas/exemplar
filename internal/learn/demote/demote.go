// Package demote is auto-demotion (spec §9.4): nightly, a rule whose
// recent feedback crosses both bars is put back in shadow through a config
// PR the bot opens and merges, naming the owner. Demotion only reduces
// behavior; promotion always requires a human.
//
// Over a rule's last 30 findings with a judgment (finding_outcomes), a
// rule is demoted when all hold:
//   - the role-weighted eFP exceeds feedback.efp_bar (0.10);
//   - the Wilson lower bound (95%) of its unweighted precision is below
//     feedback.demotion_wilson_floor (0.80);
//   - the negatives include an owner adjudication, or explicit negatives
//     from at least two distinct non-author people (author signals alone
//     only flag a rule).
package demote

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Window is how many of a rule's most recent judged findings count.
const Window = 30

// Defaults when global.yaml has no feedback section.
const (
	DefaultEFPBar      = 0.10
	DefaultWilsonFloor = 0.80
)

// z is the normal quantile of the Wilson bound: 95%, two-sided.
const z = 1.959964

// Decision is the demotion test for one rule, with its inputs.
type Decision struct {
	RuleID          string  `json:"rule_id"`
	Judged          int     `json:"judged"`
	Negatives       int     `json:"negatives"`
	EFP             float64 `json:"efp_weighted"`
	Precision       float64 `json:"precision"`
	Wilson          float64 `json:"wilson_lower"`
	Adjudicated     bool    `json:"owner_overturned"`
	NonAuthorActors int     `json:"non_author_actors"`
	Demote          bool    `json:"demote"`
	// Flag is set when both bars are crossed on author signals alone: the
	// rule is flagged for its owner, and demoted if they do not respond.
	Flag bool `json:"flag,omitempty"`
	// Why says what kept a rule from demotion, or why it is demoted.
	Why string `json:"why"`
}

// Wilson is the lower bound of the Wilson score interval for k successes
// in n trials.
func Wilson(k, n int) float64 {
	if n == 0 {
		return 0
	}
	nf := float64(n)
	p := float64(k) / nf
	z2 := z * z
	return (p + z2/(2*nf) - z*math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf))) / (1 + z2/nf)
}

// Decide applies §9.4 to one rule's stats.
func Decide(s queries.DemotionStatsRow, cfg *schema.FeedbackConfig) Decision {
	bar, floor := DefaultEFPBar, DefaultWilsonFloor
	if cfg != nil {
		if cfg.EFPBar > 0 {
			bar = cfg.EFPBar
		}
		if cfg.DemotionWilsonFlr > 0 {
			floor = cfg.DemotionWilsonFlr
		}
	}
	d := Decision{RuleID: s.RuleID, Judged: int(s.Judged), Negatives: int(s.Negatives),
		Adjudicated: s.Adjudicated, NonAuthorActors: int(s.NonAuthorActors)}
	if s.NegW+s.PosW > 0 {
		d.EFP = s.NegW / (s.NegW + s.PosW)
	}
	if d.Judged > 0 {
		d.Precision = float64(d.Judged-d.Negatives) / float64(d.Judged)
	}
	d.Wilson = Wilson(d.Judged-d.Negatives, d.Judged)
	switch {
	case d.Judged < Window:
		d.Why = fmt.Sprintf("only %d judged findings; demotion needs %d", d.Judged, Window)
	case d.EFP <= bar:
		d.Why = fmt.Sprintf("weighted eFP %.3f is within the bar %.2f", d.EFP, bar)
	case d.Wilson >= floor:
		d.Why = fmt.Sprintf("precision lower bound %.3f meets the floor %.2f", d.Wilson, floor)
	case !d.Adjudicated && d.NonAuthorActors < 2:
		d.Flag = true
		d.Why = "negatives come from the author alone (with no owner adjudication or two non-author people): flagged, not demoted"
	default:
		d.Demote = true
		d.Why = fmt.Sprintf("weighted eFP %.3f > %.2f and precision lower bound %.3f < %.2f", d.EFP, bar, d.Wilson, floor)
	}
	return d
}

// Repo is the part of exemplar-config the writer needs (gh.ConfigRepo).
type Repo interface {
	DefaultBranch(ctx context.Context) (name, sha string, err error)
	FindPR(ctx context.Context, branch string) (url string, found bool, err error)
	EnsureBranch(ctx context.Context, branch, sha string) error
	GetFile(ctx context.Context, ref, path string) ([]byte, error)
	PutFile(ctx context.Context, branch, path string, content []byte, message string) error
	OpenPR(ctx context.Context, branch, base, title, body string) (url string, number int, err error)
	MergePR(ctx context.Context, number int, message string) error
	FindIssue(ctx context.Context, label, title string) (gh.ConfigItem, bool, error)
	CreateIssue(ctx context.Context, title, body string, labels []string) (gh.ConfigItem, error)
	CloseIssue(ctx context.Context, number int, comment string) error
	HumanActivity(ctx context.Context, number int, since time.Time) (bool, error)
}

// Stats reads the demotion inputs (store.Store).
type Stats interface {
	DemotionStats(ctx context.Context, lastN int32) ([]queries.DemotionStatsRow, error)
}

// Outcome is what happened to one rule the test would demote.
type Outcome struct {
	Decision
	Action string `json:"action"` // demoted | opened | flagged | waiting | responded | skipped | dry_run
	URL    string `json:"url,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Runner demotes rules.
type Runner struct {
	Stats Stats
	// Repo writes the config PR; nil makes Run a dry run.
	Repo Repo
	Log  *slog.Logger
	Now  func() time.Time
	// ResponseDays overrides ResponseDays (feedback.owner_response_days).
	ResponseDays int
}

// Run decides every rule with judged findings and demotes those that
// cross the bars. It returns every decision, and an outcome for each rule
// it would demote. One rule's failure does not stop the rest.
func (r Runner) Run(ctx context.Context, snap *config.Snapshot) ([]Decision, []Outcome, error) {
	if r.Log == nil {
		r.Log = slog.Default()
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	rows, err := r.Stats.DemotionStats(ctx, Window)
	if err != nil {
		return nil, nil, fmt.Errorf("demote: stats: %w", err)
	}
	rules := map[string]config.Rule{}
	for _, rule := range snap.Rules {
		if rule.Scope == "global" {
			rules[rule.ID] = rule
		}
	}
	var decisions []Decision
	var outs []Outcome
	var errs []error
	for _, row := range rows {
		d := Decide(row, snap.Global.Feedback)
		decisions = append(decisions, d)
		if !d.Demote && !d.Flag {
			continue
		}
		rule, ok := rules[d.RuleID]
		switch {
		case !ok:
			// A bug class, a repo's own rule, or a rule no longer in config.
			outs = append(outs, Outcome{Decision: d, Action: "skipped", Reason: "not a rule in exemplar-config"})
			continue
		case rule.Status != schema.StatusActive:
			outs = append(outs, Outcome{Decision: d, Action: "skipped", Reason: "already " + rule.Status})
			continue
		}
		var out Outcome
		if d.Flag {
			out, err = r.flag(ctx, rule, d)
		} else {
			out, err = r.demote(ctx, rule, d)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("demote %s: %w", d.RuleID, err))
			out = Outcome{Decision: d, Action: "skipped", Reason: err.Error()}
		}
		outs = append(outs, out)
	}
	return decisions, outs, errors.Join(errs...)
}

var statusLine = regexp.MustCompile(`(?m)^status:[ \t]*["']?active["']?[ \t]*(#.*)?$`)

// shadowed rewrites a rule file's status from active to shadow, keeping
// everything else, comments included, byte for byte.
func shadowed(content []byte) ([]byte, error) {
	locs := statusLine.FindAllIndex(content, -1)
	if len(locs) != 1 {
		return nil, fmt.Errorf("expected one top-level `status: active` line, found %d", len(locs))
	}
	out := append([]byte{}, content[:locs[0][0]]...)
	out = append(out, "status: shadow"...)
	return append(out, content[locs[0][1]:]...), nil
}

func (r Runner) demote(ctx context.Context, rule config.Rule, d Decision) (Outcome, error) {
	if r.Repo == nil {
		return Outcome{Decision: d, Action: "dry_run"}, nil
	}
	// One branch per rule version: a rule re-promoted later (a new file
	// version) can be demoted again.
	branch := fmt.Sprintf("exemplar/demote/%s/%s", rule.ID, rule.VersionSHA[:min(12, len(rule.VersionSHA))])
	if url, found, err := r.Repo.FindPR(ctx, branch); err != nil {
		return Outcome{}, err
	} else if found {
		return Outcome{Decision: d, Action: "skipped", URL: url, Reason: "a demotion PR for this version exists"}, nil
	}
	base, sha, err := r.Repo.DefaultBranch(ctx)
	if err != nil {
		return Outcome{}, err
	}
	content, err := r.Repo.GetFile(ctx, base, rule.Path)
	if err != nil {
		return Outcome{}, err
	}
	next, err := shadowed(content)
	if err != nil {
		return Outcome{}, fmt.Errorf("%s: %w", rule.Path, err)
	}
	if err := r.Repo.EnsureBranch(ctx, branch, sha); err != nil {
		return Outcome{}, err
	}
	msg := fmt.Sprintf("Demote %s to shadow (spec §9.4)", rule.ID)
	if err := r.Repo.PutFile(ctx, branch, rule.Path, next, msg); err != nil {
		return Outcome{}, err
	}
	url, number, err := r.Repo.OpenPR(ctx, branch, base, msg, prBody(rule, d, r.Now()))
	if err != nil {
		return Outcome{}, err
	}
	if err := r.Repo.MergePR(ctx, number, msg); err != nil {
		// Protection that needs a review: the PR waits for a human.
		r.Log.Warn("demotion PR opened but not merged", "rule", rule.ID, "url", url, "err", err)
		return Outcome{Decision: d, Action: "opened", URL: url, Reason: "not merged: " + err.Error()}, nil
	}
	return Outcome{Decision: d, Action: "demoted", URL: url}, nil
}

func prBody(rule config.Rule, d Decision, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: Exemplar has moved `%s` to **shadow** (spec §9.4). It keeps running and storing findings, but posts none.\n\n", rule.Owner, rule.ID)
	fmt.Fprintf(&b, "Over its last %d findings with feedback (as of %s):\n\n", d.Judged, now.UTC().Format(time.DateOnly))
	fmt.Fprintf(&b, "| Measure | Value | Bar |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| Role-weighted false-positive rate | %.3f | demote above the eFP bar |\n", d.EFP)
	fmt.Fprintf(&b, "| Precision (unweighted) | %.3f (%d of %d negative) | |\n", d.Precision, d.Negatives, d.Judged)
	fmt.Fprintf(&b, "| Precision, 95%% Wilson lower bound | %.3f | demote below the floor |\n", d.Wilson)
	if d.Adjudicated {
		b.WriteString("| Owner adjudication | an owner overturned a finding | |\n")
	}
	fmt.Fprintf(&b, "| Non-author people with explicit negatives | %d | at least 2, or an adjudication |\n", d.NonAuthorActors)
	b.WriteString("\nTo put it back, fix the rule (description, scope or examples) and set `status: active` in a new PR; promotion always needs a human.\n")
	return b.String()
}

// FlagLabel labels flag issues in exemplar-config.
const FlagLabel = "exemplar-flag"

// ResponseDays is how long an owner has to respond to a flag (spec §9.4,
// feedback.owner_response_days) before the rule is demoted.
const ResponseDays = 14

// flag opens one flag issue per rule version naming the owner, and after
// ResponseDays with no response from a person (a comment, or closing it)
// demotes the rule the same way the bars do (spec §9.4).
func (r Runner) flag(ctx context.Context, rule config.Rule, d Decision) (Outcome, error) {
	if r.Repo == nil {
		return Outcome{Decision: d, Action: "dry_run"}, nil
	}
	days := ResponseDays
	if r.ResponseDays > 0 {
		days = r.ResponseDays
	}
	title := fmt.Sprintf("Exemplar flag: %s (%s)", rule.ID, rule.VersionSHA[:min(12, len(rule.VersionSHA))])
	issue, found, err := r.Repo.FindIssue(ctx, FlagLabel, title)
	if err != nil {
		return Outcome{}, err
	}
	if !found {
		deadline := r.Now().AddDate(0, 0, days)
		issue, err := r.Repo.CreateIssue(ctx, title, flagBody(rule, d, deadline), []string{FlagLabel})
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Decision: d, Action: "flagged", URL: issue.URL}, nil
	}
	if issue.State == "closed" {
		return Outcome{Decision: d, Action: "responded", URL: issue.URL, Reason: "the flag was closed"}, nil
	}
	responded, err := r.Repo.HumanActivity(ctx, issue.Number, issue.CreatedAt)
	if err != nil {
		return Outcome{}, err
	}
	switch {
	case responded:
		return Outcome{Decision: d, Action: "responded", URL: issue.URL, Reason: "the owner commented"}, nil
	case r.Now().Before(issue.CreatedAt.AddDate(0, 0, days)):
		return Outcome{Decision: d, Action: "waiting", URL: issue.URL}, nil
	}
	d.Why = fmt.Sprintf("flagged %s with no response in %d days", issue.CreatedAt.UTC().Format(time.DateOnly), days)
	out, err := r.demote(ctx, rule, d)
	if err != nil {
		return Outcome{}, err
	}
	if out.URL != "" {
		if err := r.Repo.CloseIssue(ctx, issue.Number, "No response in "+strconv.Itoa(days)+" days, so the rule moves to shadow: "+out.URL); err != nil {
			r.Log.Warn("flag issue not closed", "rule", rule.ID, "err", err)
		}
	}
	return out, nil
}

func flagBody(rule config.Rule, d Decision, deadline time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: authors keep rejecting `%s`. Over its last %d findings with feedback, the role-weighted false-positive rate is %.3f and the precision lower bound %.3f (%d of %d negative).\n\n", rule.Owner, rule.ID, d.Judged, d.EFP, d.Wilson, d.Negatives, d.Judged)
	b.WriteString("Only PR authors have objected so far, so the rule is flagged rather than demoted (spec §9.4). ")
	fmt.Fprintf(&b, "**Respond by %s** (adjudicate findings with `/exemplar uphold` or `/exemplar overturn`, change the rule, or comment or close here); otherwise Exemplar moves the rule to shadow.\n", deadline.UTC().Format(time.DateOnly))
	return b.String()
}
