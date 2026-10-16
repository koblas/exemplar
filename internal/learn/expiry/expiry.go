// Package expiry is rule expiry: a daily job finds active
// rules whose review_by date has passed. A rule whose last 90 days of
// production feedback are under the eFP bar gets a PR extending review_by
// by expiry.default_months, for its owner to merge. Any other gets a PR
// setting status: shadow, which merges after expiry.auto_demote_after_days
// unless a person responds (demotion is the safe direction).
//
// Deviation: the original design re-runs the rule's eval on cases from the
// last 90 days. This uses the last 90 days of production outcomes instead
// (finding_outcomes), so a rule with no judged findings in that window
// cannot be confirmed and goes to the shadow PR, which its owner can
// close and replace with an extension.
package expiry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Window is the evidence window, in days.
const Window = 90

// Defaults when global.yaml has no expiry or feedback section.
const (
	DefaultMonths    = 12
	DefaultAfterDays = 14
	DefaultEFPBar    = 0.10
)

// Repo is the part of exemplar-config the job needs (gh.ConfigRepo).
type Repo interface {
	DefaultBranch(ctx context.Context) (name, sha string, err error)
	PRByBranch(ctx context.Context, branch string) (gh.ConfigItem, bool, error)
	EnsureBranch(ctx context.Context, branch, sha string) error
	GetFile(ctx context.Context, ref, path string) ([]byte, error)
	PutFile(ctx context.Context, branch, path string, content []byte, message string) error
	OpenPR(ctx context.Context, branch, base, title, body string) (url string, number int, err error)
	MergePR(ctx context.Context, number int, message string) error
	HumanActivity(ctx context.Context, number int, since time.Time) (bool, error)
}

// Stats reads a rule's recent outcomes (store.Store).
type Stats interface {
	RuleRecentOutcomes(ctx context.Context, arg queries.RuleRecentOutcomesParams) (queries.RuleRecentOutcomesRow, error)
}

// Outcome is what happened to one expired rule.
type Outcome struct {
	RuleID   string  `json:"rule_id"`
	ReviewBy string  `json:"review_by"`
	Judged   int     `json:"judged"`
	EFP      float64 `json:"efp_weighted"`
	// Extend is the decision: extend review_by, or move to shadow.
	Extend bool   `json:"extend"`
	Action string `json:"action"` // proposed | waiting | responded | demoted | done | dry_run | skipped
	URL    string `json:"url,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Runner runs expiry.
type Runner struct {
	Stats Stats
	// Repo writes the PRs; nil makes Run a dry run.
	Repo Repo
	Log  *slog.Logger
	Now  func() time.Time
}

// Run handles every active global rule past its review_by date. One
// rule's failure does not stop the rest.
func (r Runner) Run(ctx context.Context, snap *config.Snapshot) ([]Outcome, error) {
	if r.Log == nil {
		r.Log = slog.Default()
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	months, after, bar := DefaultMonths, DefaultAfterDays, DefaultEFPBar
	if e := snap.Global.Expiry; e != nil {
		if e.DefaultMonths > 0 {
			months = e.DefaultMonths
		}
		if e.AutoDemoteAfterDays > 0 {
			after = e.AutoDemoteAfterDays
		}
	}
	if f := snap.Global.Feedback; f != nil && f.EFPBar > 0 {
		bar = f.EFPBar
	}
	today := r.Now().UTC().Truncate(24 * time.Hour)
	var outs []Outcome
	var errs []error
	for _, rule := range snap.Rules {
		if rule.Scope != "global" || rule.Status != schema.StatusActive || rule.ReviewBy == "" {
			continue
		}
		due, err := time.Parse(time.DateOnly, rule.ReviewBy)
		if err != nil || !due.Before(today) {
			continue
		}
		s, err := r.Stats.RuleRecentOutcomes(ctx, queries.RuleRecentOutcomesParams{RuleID: rule.ID, Days: Window})
		if err != nil {
			errs = append(errs, fmt.Errorf("expiry %s: %w", rule.ID, err))
			continue
		}
		o := Outcome{RuleID: rule.ID, ReviewBy: rule.ReviewBy, Judged: int(s.Judged)}
		if s.NegW+s.PosW > 0 {
			o.EFP = s.NegW / (s.NegW + s.PosW)
		}
		o.Extend = o.Judged > 0 && o.EFP <= bar
		out, err := r.handle(ctx, rule, o, today.AddDate(0, months, 0), after)
		if err != nil {
			errs = append(errs, fmt.Errorf("expiry %s: %w", rule.ID, err))
			out = o
			out.Action, out.Reason = "skipped", err.Error()
		}
		outs = append(outs, out)
	}
	return outs, errors.Join(errs...)
}

var (
	reviewByLine = regexp.MustCompile(`(?m)^review_by:[ \t]*["']?\d{4}-\d{2}-\d{2}["']?[ \t]*(#.*)?$`)
	activeLine   = regexp.MustCompile(`(?m)^status:[ \t]*["']?active["']?[ \t]*(#.*)?$`)
)

var errRewriteMatch = errors.New("expected one matching line")

// rewrite replaces the one line re matches with line.
func rewrite(content []byte, re *regexp.Regexp, line string) ([]byte, error) {
	locs := re.FindAllIndex(content, -1)
	if len(locs) != 1 {
		return nil, fmt.Errorf("%w: %s, found %d", errRewriteMatch, re, len(locs))
	}
	out := append([]byte{}, content[:locs[0][0]]...)
	out = append(out, line...)
	return append(out, content[locs[0][1]:]...), nil
}

func (r Runner) handle(ctx context.Context, rule config.Rule, o Outcome, until time.Time, afterDays int) (Outcome, error) {
	if r.Repo == nil {
		o.Action = "dry_run"
		return o, nil
	}
	kind := "shadow"
	if o.Extend {
		kind = "extend"
	}
	// One branch per rule version and decision: a rule changed by its
	// owner (a new version) is judged afresh.
	branch := fmt.Sprintf("exemplar/expiry/%s/%s/%s", rule.ID, kind, rule.VersionSHA[:min(12, len(rule.VersionSHA))])
	pr, found, err := r.Repo.PRByBranch(ctx, branch)
	if err != nil {
		return o, fmt.Errorf("find PR by branch: %w", err)
	}
	if found {
		o.URL = pr.URL
		switch {
		case pr.State == "closed":
			o.Action, o.Reason = "done", "closed or merged"
			return o, nil
		case o.Extend:
			o.Action = "waiting" // an extension waits for its owner
			return o, nil
		}
		responded, err := r.Repo.HumanActivity(ctx, pr.Number, pr.CreatedAt)
		if err != nil {
			return o, fmt.Errorf("check human activity: %w", err)
		}
		switch {
		case responded:
			o.Action = "responded" // the owner engaged; a human decides
		case r.Now().Before(pr.CreatedAt.AddDate(0, 0, afterDays)):
			o.Action = "waiting"
		default:
			if err := r.Repo.MergePR(ctx, pr.Number, "Expired rule "+rule.ID+" moves to shadow (spec §8.10)"); err != nil {
				return o, fmt.Errorf("merge PR: %w", err)
			}
			o.Action = "demoted"
		}
		return o, nil
	}
	base, sha, err := r.Repo.DefaultBranch(ctx)
	if err != nil {
		return o, fmt.Errorf("get default branch: %w", err)
	}
	content, err := r.Repo.GetFile(ctx, base, rule.Path)
	if err != nil {
		return o, fmt.Errorf("get file: %w", err)
	}
	var next []byte
	var title string
	if o.Extend {
		next, err = rewrite(content, reviewByLine, "review_by: "+until.Format(time.DateOnly))
		title = fmt.Sprintf("Extend %s's review date to %s (spec §8.10)", rule.ID, until.Format(time.DateOnly))
	} else {
		next, err = rewrite(content, activeLine, "status: shadow")
		title = fmt.Sprintf("Move expired rule %s to shadow (spec §8.10)", rule.ID)
	}
	if err != nil {
		return o, fmt.Errorf("%s: %w", rule.Path, err)
	}
	if err := r.Repo.EnsureBranch(ctx, branch, sha); err != nil {
		return o, fmt.Errorf("ensure branch: %w", err)
	}
	if err := r.Repo.PutFile(ctx, branch, rule.Path, next, title); err != nil {
		return o, fmt.Errorf("put file: %w", err)
	}
	url, _, err := r.Repo.OpenPR(ctx, branch, base, title, body(rule, o, until, afterDays, r.Now()))
	if err != nil {
		return o, fmt.Errorf("open PR: %w", err)
	}
	o.Action, o.URL = "proposed", url
	return o, nil
}

func body(rule config.Rule, o Outcome, until time.Time, afterDays int, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: `%s` passed its review date (%s).\n\n", rule.Owner, rule.ID, o.ReviewBy)
	if o.Extend {
		fmt.Fprintf(&b, "Over the last %d days it had %d findings with feedback and a role-weighted false-positive rate of %.3f, under the bar. "+
			"Merge this PR to extend `review_by` to %s; Exemplar will not merge it for you.\n", Window, o.Judged, o.EFP, until.Format(time.DateOnly))
		return b.String()
	}
	if o.Judged == 0 {
		fmt.Fprintf(&b, "It had no findings with feedback in the last %d days, so Exemplar cannot confirm it still holds.", Window)
	} else {
		fmt.Fprintf(&b, "Over the last %d days its role-weighted false-positive rate was %.3f, over the bar.", Window, o.EFP)
	}
	fmt.Fprintf(&b, " This PR moves it to **shadow**. It merges on its own after %s (%d days) unless you comment, review or close it. "+
		"To keep the rule active, close this PR and extend `review_by` in a new one.\n",
		now.AddDate(0, 0, afterDays).UTC().Format(time.DateOnly), afterDays)
	return b.String()
}
