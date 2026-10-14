// Package digest writes the weekly rule-health digest for each rule owner
// (FR-B5, PRD §17.8, spec §9.4): an issue in exemplar-config per owner
// team, from the same views the dashboards and auto-demotion read.
package digest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/learn/demote"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/upstream"
)

// Window is the digest's stats window, in days.
const Window = 28

// Label labels digest issues.
const Label = "exemplar-digest"

// closeTo is how near the eFP bar a rule is "close" (spec §12.5: within
// 5 points of the demotion threshold).
const closeTo = 0.05

// Store reads the views (store.Store).
type Store interface {
	DigestRuleStats(ctx context.Context, days int32) ([]queries.DigestRuleStatsRow, error)
	DigestDismissals(ctx context.Context, days int32) ([]queries.DigestDismissalsRow, error)
	DigestAdjudicationQueue(ctx context.Context) ([]queries.DigestAdjudicationQueueRow, error)
	DigestAutoApproved(ctx context.Context, arg queries.DigestAutoApprovedParams) ([]queries.DigestAutoApprovedRow, error)
	DemotionStats(ctx context.Context, lastN int32) ([]queries.DemotionStatsRow, error)
	ExportDrift(ctx context.Context, versionsBehind int32) ([]queries.ExportDriftRow, error)
	UpstreamRuleStats(ctx context.Context, days int32) ([]queries.UpstreamRuleStatsRow, error)
}

// DriftBehind is how many config versions behind a repo's exports must be
// to reach the platform digest (spec §10.2: more than two).
const DriftBehind = 3

// Repo opens the issues (gh.ConfigRepo).
type Repo interface {
	FindIssue(ctx context.Context, label, title string) (gh.ConfigItem, bool, error)
	CreateIssue(ctx context.Context, title, body string, labels []string) (gh.ConfigItem, error)
}

// Digest is one owner's issue.
type Digest struct {
	Owner string `json:"owner"`
	Title string `json:"title"`
	Body  string `json:"-"`
	// Action is opened, exists or dry_run.
	Action string `json:"action"`
	URL    string `json:"url,omitempty"`
}

// Runner writes the digests.
type Runner struct {
	Store Store
	// Repo opens the issues; nil makes Run a dry run.
	Repo Repo
	// Platform, when set, also gets the export-drift digest (spec §10.2).
	Platform string
	// GitHubURL is the web base for links (default https://github.com/).
	GitHubURL string
	Log       *slog.Logger
	Now       func() time.Time
}

// Run writes one digest per owner of a global rule that is active or in
// shadow. The title names the week (its Monday), so a second run that
// week finds the issue and opens no other.
func (r Runner) Run(ctx context.Context, snap *config.Snapshot) ([]Digest, error) {
	if r.Log == nil {
		r.Log = slog.Default()
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.GitHubURL == "" {
		r.GitHubURL = "https://github.com/"
	}
	in, err := r.load(ctx, snap)
	if err != nil {
		return nil, err
	}
	monday := r.Now().UTC().Truncate(24 * time.Hour)
	for monday.Weekday() != time.Monday {
		monday = monday.AddDate(0, 0, -1)
	}
	owners := map[string][]config.Rule{}
	for _, rule := range snap.Rules {
		if rule.Scope == "global" && (rule.Status == schema.StatusActive || rule.Status == schema.StatusShadow) {
			owners[rule.Owner] = append(owners[rule.Owner], rule)
		}
	}
	names := make([]string, 0, len(owners))
	for o := range owners {
		names = append(names, o)
	}
	slices.Sort(names)
	var out []Digest
	var errs []error
	var pending []Digest
	for _, owner := range names {
		approved, err := r.Store.DigestAutoApproved(ctx, queries.DigestAutoApprovedParams{Owner: owner, Days: 7})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		d := Digest{Owner: owner, Title: fmt.Sprintf("Rule health for %s, week of %s", owner, monday.Format("January 2, 2006"))}
		d.Body = r.body(owner, owners[owner], in, approved, r.Now(), snap.Global.Feedback)
		pending = append(pending, d)
	}
	if r.Platform != "" {
		drift, err := r.Store.ExportDrift(ctx, DriftBehind)
		switch {
		case err != nil:
			errs = append(errs, err)
		case len(drift) > 0:
			pending = append(pending, Digest{Owner: r.Platform, Title: fmt.Sprintf("Export drift, week of %s", monday.Format("January 2, 2006")),
				Body: driftBody(r.Platform, drift)})
		}
	}
	for _, d := range pending {
		if r.Repo == nil {
			d.Action = "dry_run"
			out = append(out, d)
			continue
		}
		issue, found, err := r.Repo.FindIssue(ctx, Label, d.Title)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !found {
			if issue, err = r.Repo.CreateIssue(ctx, d.Title, d.Body, []string{Label}); err != nil {
				errs = append(errs, err)
				continue
			}
			d.Action = "opened"
		} else {
			d.Action = "exists"
		}
		d.URL = issue.URL
		out = append(out, d)
	}
	return out, errors.Join(errs...)
}

type inputs struct {
	stats      map[string]queries.DigestRuleStatsRow
	dismissals map[string][]queries.DigestDismissalsRow
	queue      map[string][]queries.DigestAdjudicationQueueRow
	decisions  map[string]demote.Decision
	upstream   map[string]upstream.Verdict
}

func (r Runner) load(ctx context.Context, snap *config.Snapshot) (inputs, error) {
	in := inputs{stats: map[string]queries.DigestRuleStatsRow{}, dismissals: map[string][]queries.DigestDismissalsRow{},
		queue: map[string][]queries.DigestAdjudicationQueueRow{}, decisions: map[string]demote.Decision{}, upstream: map[string]upstream.Verdict{}}
	stats, err := r.Store.DigestRuleStats(ctx, Window)
	if err != nil {
		return in, err
	}
	for _, s := range stats {
		in.stats[s.RuleID] = s
	}
	dis, err := r.Store.DigestDismissals(ctx, Window)
	if err != nil {
		return in, err
	}
	for _, d := range dis {
		in.dismissals[d.RuleID] = append(in.dismissals[d.RuleID], d)
	}
	q, err := r.Store.DigestAdjudicationQueue(ctx)
	if err != nil {
		return in, err
	}
	for _, f := range q {
		in.queue[f.RuleID] = append(in.queue[f.RuleID], f)
	}
	ds, err := r.Store.DemotionStats(ctx, demote.Window)
	if err != nil {
		return in, err
	}
	for _, s := range ds {
		in.decisions[s.RuleID] = demote.Decide(s, snap.Global.Feedback)
	}
	ups, err := r.Store.UpstreamRuleStats(ctx, upstream.Window)
	if err != nil {
		return in, err
	}
	for _, u := range ups {
		in.upstream[u.RuleID] = upstream.Judge(
			upstream.Comparison{Name: "PRs whose agent read the export, against the rest", WithPRs: int(u.ReceivedPrs), WithViolations: int(u.ReceivedViolations),
				WithoutPRs: int(u.OtherPrs), WithoutViols: int(u.OtherViolations)},
			upstream.Comparison{Name: "exporting repos after the export landed, against before", WithPRs: int(u.AfterPrs), WithViolations: int(u.AfterViolations),
				WithoutPRs: int(u.BeforePrs), WithoutViols: int(u.BeforeViolations)})
	}
	return in, nil
}

var reasonNames = map[string]string{
	"fb_not_important": "Not important here", "fb_handled": "Already handled", "fb_wrong": "Wrong", "fb_disagree": "Disagree with rule",
}

func pct(n, d int) string {
	if d == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(d))
}

func (r Runner) body(owner string, rules []config.Rule, in inputs, approved []queries.DigestAutoApprovedRow, now time.Time, fb *schema.FeedbackConfig) string {
	bar := demote.DefaultEFPBar
	if fb != nil && fb.EFPBar > 0 {
		bar = fb.EFPBar
	}
	slices.SortFunc(rules, func(a, b config.Rule) int { return cmp.Compare(a.ID, b.ID) })
	var b strings.Builder
	fmt.Fprintf(&b, "%s: your rules over the last %d days (spec §9.4). Generated from the same views as the dashboards and auto-demotion.\n\n", owner, Window)
	b.WriteString("| Rule | Status | Posted | Accepted | Effective FP | Health |\n|---|---|---|---|---|---|\n")
	today := now.UTC().Format(time.DateOnly)
	for _, rule := range rules {
		s := in.stats[rule.ID]
		efp, efpText := 0.0, "—"
		if s.NegW+s.PosW > 0 {
			efp = s.NegW / (s.NegW + s.PosW)
			efpText = fmt.Sprintf("%.0f%%", 100*efp)
		}
		d := in.decisions[rule.ID]
		var health string
		switch {
		case rule.ReviewBy != "" && rule.ReviewBy < today:
			health = "Review date passed (" + rule.ReviewBy + "). Confirm or retire."
		case d.Demote:
			health = "Over the bar: Exemplar is moving it to shadow (a config PR names you)."
		case d.Flag:
			health = fmt.Sprintf("Over the bar on authors' signals. Respond to the flag issue within %d days or it moves to shadow.", demote.ResponseDays)
		case efpText == "—":
			health = "No feedback yet"
		case efp > bar:
			health = "Over the bar (not enough judged findings to demote yet)"
		case efp >= bar-closeTo:
			health = fmt.Sprintf("Close to the %.0f%% bar", 100*bar)
		default:
			health = "Healthy"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %d | %s | %s | %s |\n", rule.ID, rule.Status, s.Posted, pct(int(s.Accepted), int(s.Judged)), efpText, health)
	}
	// Most common dismissal reasons, with where they cluster (the first two
	// path segments): a context gap or a scope to narrow.
	var reasons []string
	for _, rule := range rules {
		byKind := map[string]int{}
		paths := map[string]map[string]int{}
		for _, d := range in.dismissals[rule.ID] {
			byKind[d.Kind] += int(d.N)
			if paths[d.Kind] == nil {
				paths[d.Kind] = map[string]int{}
			}
			paths[d.Kind][prefix(d.Path)] += int(d.N)
		}
		kinds := make([]string, 0, len(byKind))
		for k := range byKind {
			kinds = append(kinds, k)
		}
		slices.SortFunc(kinds, func(a, b string) int { return cmp.Or(cmp.Compare(byKind[b], byKind[a]), cmp.Compare(a, b)) })
		if len(kinds) == 0 {
			continue
		}
		top := kinds[0]
		where, most := "", 0
		for p, n := range paths[top] {
			if n > most || n == most && p < where {
				where, most = p, n
			}
		}
		line := fmt.Sprintf("`%s`: *%s* (%d)", rule.ID, reasonNames[top], byKind[top])
		if most*2 > byKind[top] {
			line += ", mostly under `" + where + "`"
		}
		reasons = append(reasons, line)
	}
	if len(reasons) > 0 {
		b.WriteString("\n**Most common dismissal reasons**\n\n")
		for _, l := range reasons {
			b.WriteString("- " + l + "\n")
		}
	}
	var waiting []string
	for _, rule := range rules {
		for _, f := range in.queue[rule.ID] {
			link := fmt.Sprintf("%s%s/pull/%d", r.GitHubURL, f.Repo, f.PrNumber)
			if f.GhCommentID != 0 {
				link += fmt.Sprintf("#discussion_r%d", f.GhCommentID)
			}
			waiting = append(waiting, fmt.Sprintf("`%s` on [%s#%d](%s): the author says *%s*", rule.ID, f.Repo, f.PrNumber, link, reasonNames[f.Kind]))
		}
	}
	if len(waiting) > 0 {
		fmt.Fprintf(&b, "\n**Waiting for your adjudication** (%d). Reply `/exemplar uphold` or `/exemplar overturn` in the thread.\n\n", len(waiting))
		for _, l := range waiting {
			b.WriteString("- " + l + "\n")
		}
	}
	var rewrite []string
	for _, rule := range rules {
		if v := in.upstream[rule.ID]; v.Rewrite && rule.Export != nil && rule.Export.Agents {
			rewrite = append(rewrite, fmt.Sprintf("`%s`: %s", rule.ID, strings.Join(v.Evidence, "; ")))
		}
	}
	if len(rewrite) > 0 {
		fmt.Fprintf(&b, "\n**Exported summaries to rewrite** (spec §10.4, last %d days). Coding agents get these rules' `export.summary`, but the violation rate did not drop by at least %.0f%%:\n\n", upstream.Window, 100*(1-upstream.Drop))
		for _, l := range rewrite {
			b.WriteString("- " + l + "\n")
		}
	}
	if len(approved) > 0 {
		fmt.Fprintf(&b, "\n**Auto-approved changes in your paths** (%d, last 7 days)\n\n", len(approved))
		for _, a := range approved {
			fmt.Fprintf(&b, "- [%s#%d](%s%s/pull/%d) at `%s`: %s\n", a.Repo, a.PrNumber, r.GitHubURL, a.Repo, a.PrNumber, a.Head, a.Paths)
		}
	}
	return b.String()
}

// prefix is a path's first two segments, or the directory of a shallow one.
func prefix(p string) string {
	parts := strings.Split(p, "/")
	switch {
	case len(parts) <= 1:
		return "/"
	case len(parts) == 2:
		return parts[0] + "/**"
	}
	return parts[0] + "/" + parts[1] + "/**"
}

// driftBody lists repos whose exported instruction files are more than
// two config versions behind (spec §10.2).
func driftBody(platform string, drift []queries.ExportDriftRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: these repos' coding-agent instruction files are more than two config versions behind the rules (spec §10.2). Same view as the shift-left board.\n\n", platform)
	b.WriteString("| Repo | Versions behind | Waiting on | Export PR |\n|---|---|---|---|\n")
	for _, d := range drift {
		var why []string
		if d.Proposed > 0 {
			why = append(why, fmt.Sprintf("%d proposed, not merged", d.Proposed))
		}
		if d.Edited > 0 {
			why = append(why, fmt.Sprintf("%d edited by hand inside the markers", d.Edited))
		}
		pr := cmp.Or(d.PrUrl, "—")
		fmt.Fprintf(&b, "| %s | %d | %s | %s |\n", d.Repo, d.VersionsBehind, strings.Join(why, "; "), pr)
	}
	b.WriteString("\nA proposed file needs its repo's owners to merge the export PR. A hand-edited file stays as it is until someone moves the text outside the markers or restores the block.\n")
	return b.String()
}
