// Package teach is the teach-the-bot fast path and routing by reason.
// Nightly, for each rule:
//   - scope: "not important here" dismissals and reasons classed scope that
//     cluster under one path prefix become a draft config PR adding a
//     file_patterns exclusion for it;
//   - exception and rule_wrong: reasons so classed open an owner review
//     issue (the description amendment and new negative eval case
//     for exception are left to the owner);
//   - disagree: three "disagree with rule" open an owner review issue.
//
// A class proposes when it recurs at least three times in 30 days, or
// once from the rule's owner. Proposals carry finding links and counts,
// never people's reasons: exemplar-config is readable by more people than
// most target repos. Nothing changes until a human merges.
package teach

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Window is the evidence window, in days.
const Window = 30

// MinSignals is how often a class must recur (unless the owner gave it).
const MinSignals = 3

// Label labels teach issues.
const Label = "exemplar-teach"

// Store reads and records (store.Store).
type Store interface {
	TeachSignals(ctx context.Context, days int32) ([]queries.TeachSignalsRow, error)
	TeachProposalExists(ctx context.Context, arg queries.TeachProposalExistsParams) (bool, error)
	InsertTeachProposal(ctx context.Context, arg queries.InsertTeachProposalParams) (int64, error)
}

// Repo writes to exemplar-config (gh.ConfigRepo).
type Repo interface {
	DefaultBranch(ctx context.Context) (name, sha string, err error)
	EnsureBranch(ctx context.Context, branch, sha string) error
	GetFile(ctx context.Context, ref, path string) ([]byte, error)
	PutFile(ctx context.Context, branch, path string, content []byte, message string) error
	OpenDraftPR(ctx context.Context, branch, base, title, body string) (string, error)
	CreateIssue(ctx context.Context, title, body string, labels []string) (gh.ConfigItem, error)
}

// Proposal is one proposal made, or that a dry run would make.
type Proposal struct {
	RuleID   string `json:"rule_id"`
	Class    string `json:"class"`
	Signals  int    `json:"signals"`
	Owner    bool   `json:"from_owner"`
	Exclude  string `json:"exclude,omitempty"` // scope: the file_patterns exclusion
	Action   string `json:"action"`            // pr | issue | dry_run | skipped
	URL      string `json:"url,omitempty"`
	Reason   string `json:"reason,omitempty"`
	evidence []queries.TeachSignalsRow
}

// Runner makes proposals.
type Runner struct {
	Store Store
	// Repo writes them; nil makes Run a dry run.
	Repo      Repo
	GitHubURL string
	Log       *slog.Logger
}

// Run makes the proposals the last 30 days support. One failure does not
// stop the rest.
func (r Runner) Run(ctx context.Context, snap *config.Snapshot) ([]Proposal, error) {
	if r.Log == nil {
		r.Log = slog.Default()
	}
	if r.GitHubURL == "" {
		r.GitHubURL = "https://github.com/"
	}
	rows, err := r.Store.TeachSignals(ctx, Window)
	if err != nil {
		return nil, err
	}
	rules := map[string]config.Rule{}
	for _, rule := range snap.Rules {
		if rule.Scope == "global" && rule.Status != schema.StatusKilled {
			rules[rule.ID] = rule
		}
	}
	type key struct{ rule, class string }
	groups := map[key][]queries.TeachSignalsRow{}
	var keys []key
	for _, s := range rows {
		k := key{s.RuleID, s.Class}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], s)
	}
	var out []Proposal
	var errs []error
	for _, k := range keys {
		rule, ok := rules[k.rule]
		if !ok {
			continue // a bug class, a repo's own rule, or a rule gone from config
		}
		p, ok := propose(k.class, groups[k], rule)
		if !ok {
			continue
		}
		exists, err := r.Store.TeachProposalExists(ctx, queries.TeachProposalExistsParams{RuleID: rule.ID, RuleVersion: rule.VersionSHA, TeachClass: p.Class})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if exists {
			continue // proposed for this version already
		}
		if err := r.make(ctx, rule, &p); err != nil {
			errs = append(errs, fmt.Errorf("teach %s %s: %w", rule.ID, p.Class, err))
			p.Action, p.Reason = "skipped", err.Error()
		}
		out = append(out, p)
	}
	return out, errors.Join(errs...)
}

// propose decides whether a class's evidence is enough, and for scope
// which prefix to exclude.
func propose(class string, ev []queries.TeachSignalsRow, rule config.Rule) (Proposal, bool) {
	owner := slices.ContainsFunc(ev, func(s queries.TeachSignalsRow) bool { return s.Role == "owner" })
	p := Proposal{RuleID: rule.ID, Class: class, Signals: len(ev), Owner: owner, evidence: ev}
	if class != "scope" {
		return p, len(ev) >= MinSignals || owner
	}
	// Scope: the prefix most of the evidence shares, checked on its own.
	by := map[string][]queries.TeachSignalsRow{}
	for _, s := range ev {
		pf := prefix(s.Path)
		by[pf] = append(by[pf], s)
	}
	best := ""
	for pf, rows := range by {
		if best == "" || len(rows) > len(by[best]) || len(rows) == len(by[best]) && pf < best {
			best = pf
		}
	}
	rows := by[best]
	p.Signals, p.evidence = len(rows), rows
	p.Owner = slices.ContainsFunc(rows, func(s queries.TeachSignalsRow) bool { return s.Role == "owner" })
	if best == "" || len(rows) < MinSignals && !p.Owner {
		return p, false
	}
	p.Exclude = "!" + best
	if slices.Contains(rule.FilePatterns, p.Exclude) {
		return p, false // already excluded
	}
	return p, true
}

// prefix is the directory glob the path's first two segments name.
func prefix(path string) string {
	parts := strings.Split(path, "/")
	switch {
	case len(parts) <= 1:
		return ""
	case len(parts) == 2:
		return parts[0] + "/**"
	}
	return parts[0] + "/" + parts[1] + "/**"
}

func (r Runner) make(ctx context.Context, rule config.Rule, p *Proposal) error {
	ids := make([]int64, len(p.evidence))
	for i, s := range p.evidence {
		ids[i] = s.FindingID
	}
	if r.Repo == nil {
		p.Action = "dry_run"
		return nil
	}
	state := "issue"
	if p.Class == "scope" {
		url, err := r.scopePR(ctx, rule, *p)
		if err != nil {
			return err
		}
		p.Action, p.URL, state = "pr", url, "proposed"
	} else {
		issue, err := r.Repo.CreateIssue(ctx, fmt.Sprintf("Exemplar: owners, please review %s (%s)", rule.ID, p.Class), r.issueBody(rule, *p), []string{Label})
		if err != nil {
			return err
		}
		p.Action, p.URL = "issue", issue.URL
	}
	_, err := r.Store.InsertTeachProposal(ctx, queries.InsertTeachProposalParams{RuleID: rule.ID, RuleVersion: rule.VersionSHA, TeachClass: p.Class,
		EvidenceFindings: ids, ConfigPrUrl: &p.URL, State: state})
	return err
}

var (
	flowPatterns  = regexp.MustCompile(`(?m)^file_patterns:[ \t]*\[(.*)\][ \t]*(#.*)?$`)
	blockPatterns = regexp.MustCompile(`(?m)^file_patterns:[ \t]*(#.*)?\n((?:[ \t]+-[^\n]*\n?)+)`)
)

// excluded adds an exclusion to a rule file's file_patterns, as a flow
// list on one line or a block list, keeping everything else.
func excluded(content []byte, pattern string) ([]byte, error) {
	quoted := fmt.Sprintf("%q", pattern)
	if m := flowPatterns.FindSubmatchIndex(content); m != nil && len(flowPatterns.FindAllIndex(content, -1)) == 1 {
		inner := strings.TrimSpace(string(content[m[2]:m[3]]))
		sep := ", "
		if inner == "" {
			sep = ""
		}
		out := append([]byte{}, content[:m[3]]...)
		out = append(out, sep+quoted...)
		return append(out, content[m[3]:]...), nil
	}
	if m := blockPatterns.FindSubmatchIndex(content); m != nil && len(blockPatterns.FindAllIndex(content, -1)) == 1 {
		items := string(content[m[4]:m[5]])
		indent := items[:len(items)-len(strings.TrimLeft(items, " \t"))]
		line := indent + "- " + quoted + "\n"
		if !strings.HasSuffix(items, "\n") {
			line = "\n" + strings.TrimSuffix(line, "\n")
		}
		out := append([]byte{}, content[:m[5]]...)
		out = append(out, line...)
		return append(out, content[m[5]:]...), nil
	}
	return nil, errors.New("no single top-level file_patterns list to extend")
}

func (r Runner) scopePR(ctx context.Context, rule config.Rule, p Proposal) (string, error) {
	base, sha, err := r.Repo.DefaultBranch(ctx)
	if err != nil {
		return "", err
	}
	content, err := r.Repo.GetFile(ctx, base, rule.Path)
	if err != nil {
		return "", err
	}
	next, err := excluded(content, p.Exclude)
	if err != nil {
		return "", fmt.Errorf("%s: %w", rule.Path, err)
	}
	branch := fmt.Sprintf("exemplar/teach/%s/scope/%s", rule.ID, rule.VersionSHA[:min(12, len(rule.VersionSHA))])
	if err := r.Repo.EnsureBranch(ctx, branch, sha); err != nil {
		return "", err
	}
	title := fmt.Sprintf("Narrow %s: exclude %s (teach-the-bot)", rule.ID, strings.TrimPrefix(p.Exclude, "!"))
	if err := r.Repo.PutFile(ctx, branch, rule.Path, next, title); err != nil {
		return "", err
	}
	return r.Repo.OpenDraftPR(ctx, branch, base, title, r.prBody(rule, p))
}

func (r Runner) links(p Proposal) string {
	var b strings.Builder
	rows := slices.Clone(p.evidence)
	slices.SortFunc(rows, func(a, b queries.TeachSignalsRow) int { return cmp.Compare(a.FindingID, b.FindingID) })
	for i, s := range rows {
		if i == 10 {
			fmt.Fprintf(&b, "- …and %d more\n", len(rows)-10)
			break
		}
		link := fmt.Sprintf("%s%s/pull/%d", r.GitHubURL, s.Repo, s.PrNumber)
		if s.GhCommentID != 0 {
			link += fmt.Sprintf("#discussion_r%d", s.GhCommentID)
		}
		fmt.Fprintf(&b, "- [%s#%d](%s), `%s` (%s)\n", s.Repo, s.PrNumber, link, s.Path, s.Role)
	}
	return b.String()
}

func (r Runner) prBody(rule config.Rule, p Proposal) string {
	who := fmt.Sprintf("%d people dismissed its findings", p.Signals)
	if p.Owner {
		who = "its owner dismissed its findings"
	}
	return fmt.Sprintf("%s: in the last %d days %s under `%s` as out of scope (\"not important here\", or a reason classed as scope). This draft adds `%s` to `file_patterns` (spec §8.7 teach-the-bot). Nothing changes until you merge it; the eval CI on this PR shows the effect.\n\nEvidence:\n\n%s",
		rule.Owner, Window, who, strings.TrimPrefix(p.Exclude, "!"), p.Exclude, r.links(p))
}

func (r Runner) issueBody(rule config.Rule, p Proposal) string {
	what := map[string]string{
		"exception":  "say the flagged code is a legitimate case the rule should allow. Consider amending the description and adding the case as a negative eval case.",
		"rule_wrong": "say the rule's guidance is mistaken or outdated.",
		"disagree":   "disagree with the rule.",
	}[p.Class]
	who := fmt.Sprintf("%d people", p.Signals)
	if p.Owner && p.Signals < MinSignals {
		who = "the rule's owner"
	}
	return fmt.Sprintf("%s: in the last %d days, %s %s Please review `%s` (spec §9.3); close this when done.\n\nEvidence:\n\n%s",
		rule.Owner, Window, who, what, rule.ID, r.links(p))
}
