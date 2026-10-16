package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/koblas/exemplar/internal/codeowners"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/exclude"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
)

// PRInfo describes the pull request under review.
type PRInfo struct {
	Repo    string
	Number  int
	Title   string
	Body    string
	Author  string
	Draft   bool
	BaseSHA string
	HeadSHA string

	// Facts for the deterministic auto-approve policy checks, from
	// GitHub. Each left empty is unknown, and the gates fail closed on it.
	//
	// AuthorType is the author's GitHub account type ("User", "Bot").
	AuthorType string
	// HeadRepo is the head branch's repository; it differs from Repo for
	// fork PRs.
	HeadRepo string
	// CommitMessages are the PR's commit messages (Co-authored-by trailers).
	CommitMessages []string
	// Labels are the PR's labels (agent marks).
	Labels []string
}

// Exclusion is a path that is not reviewed, with the reason.
type Exclusion struct {
	Path   string
	Reason string
}

// ScopedRule is a rule with the files it covers in this review.
type ScopedRule struct {
	Rule  config.Rule
	Lane  string
	Files []string
}

// Prepared is a PR ready for the lanes. Every text in it that can reach a
// model has been secret-redacted.
type Prepared struct {
	PR        PRInfo
	Effective *config.Effective
	Diff      *scope.Diff
	Workspace *Workspace

	// Title and Body are redacted.
	Title, Body string
	// Hunks maps each reviewable path to its redacted hunk text.
	Hunks    map[string]string
	Files    []string // reviewable paths, diff order
	Excluded []Exclusion

	Scoped   []ScopedRule
	Deferred []ScopedRule

	// Secrets are deterministic possible-secret-in-diff findings.
	Secrets []Finding
	// search is the exemplar-search pin Review set, if any.
	search *searchPin

	// CodeOwners is the base commit's CODEOWNERS (empty when it has none),
	// for the auto-approve owner lookup.
	CodeOwners codeowners.File

	// Incomplete is set when an input could not be redacted; such a run
	// sends nothing to a model and posts nothing.
	Incomplete string
}

// ErrIneligible wraps every reason a PR is not reviewed at all.
var ErrIneligible = errors.New("engine: not eligible for review")

const maxFileBytes = 1 << 20

// Prepare runs eligibility, exclusions, secret redaction and scoping for
// one PR.
func Prepare(ctx context.Context, ws *Workspace, snap *config.Snapshot, pr PRInfo, scanner *redact.Scanner) (*Prepared, error) {
	base, err := config.GitFS(ctx, ws.Dir, ws.BaseSHA, append([]string{config.RepoRulesDir, ".gitattributes", ".exemplarignore", ".gitleaksignore"}, codeowners.Locations...)...)
	if err != nil {
		return nil, fmt.Errorf("engine: read base config: %w", err)
	}
	repoRules, err := config.LoadRepoRules(base, pr.Repo)
	if err != nil {
		return nil, fmt.Errorf("engine: repo rules: %w", err)
	}
	eff, err := snap.Effective(pr.Repo, repoRules)
	if err != nil {
		if errors.Is(err, config.ErrNotOptedIn) || errors.Is(err, config.ErrDenylisted) ||
			errors.Is(err, config.ErrRepoDisabled) || errors.Is(err, config.ErrDisabledGlobally) {
			return nil, fmt.Errorf("%w: %w", ErrIneligible, err)
		}
		return nil, err
	}
	if pr.Draft && !eff.RepoConfig.ReviewDrafts {
		return nil, fmt.Errorf("%w: draft PR and review_drafts is off", ErrIneligible)
	}
	// Fork PRs only when the repo opts in. An unknown head repo
	// (replays) is not treated as a fork; the worker sets it from GitHub.
	if pr.HeadRepo != "" && !strings.EqualFold(pr.HeadRepo, pr.Repo) && (eff.RepoConfig.Forks == nil || !eff.RepoConfig.Forks.Review) {
		return nil, fmt.Errorf("%w: fork PR and forks.review is off", ErrIneligible)
	}

	diff, err := ws.Diff(ctx)
	if err != nil {
		return nil, err
	}
	p := &Prepared{PR: pr, Effective: eff, Diff: diff, Workspace: ws, Hunks: map[string]string{}, CodeOwners: baseCodeOwners(base)}

	rules := exclude.Load(fileText(base, ".gitattributes"), fileText(base, ".exemplarignore"))
	gitleaksIgnore := parseGitleaksIgnore(fileText(base, ".gitleaksignore"))

	for _, f := range diff.Files() {
		if reason := p.exclusion(ctx, f, rules); reason != "" {
			p.Excluded = append(p.Excluded, Exclusion{Path: f.Path, Reason: reason})
			continue
		}
		var raw strings.Builder
		for _, h := range f.Hunks {
			raw.WriteString(h.Text)
		}
		redacted, matches, err := scanner.Redact(raw.String())
		if err != nil {
			p.Incomplete = "could not redact a secret in " + f.Path
			return p, nil
		}
		p.Hunks[f.Path] = redacted
		p.Files = append(p.Files, f.Path)
		lines := hunkLineMap(raw.String())
		for _, m := range matches {
			loc, ok := lines[m.Line]
			if !ok {
				continue
			}
			if gitleaksIgnore[f.Path+":"+m.RuleID+":"+strconv.Itoa(loc.line)] {
				continue // allowlisted: still redacted, no finding
			}
			p.Secrets = append(p.Secrets, secretFinding(f.Path, loc, m.RuleID))
		}
	}

	var ok bool
	if p.Title, ok = redactField(scanner, pr.Title); !ok {
		p.Incomplete = "could not redact a secret in the PR title"
		return p, nil
	}
	if p.Body, ok = redactField(scanner, pr.Body); !ok {
		p.Incomplete = "could not redact a secret in the PR description"
		return p, nil
	}

	p.scope()
	return p, nil
}

func redactField(s *redact.Scanner, text string) (string, bool) {
	out, _, err := s.Redact(text)
	return out, err == nil
}

func (p *Prepared) exclusion(ctx context.Context, f scope.File, rules exclude.Rules) string {
	switch {
	case f.Status == scope.StatusRemoved:
		return "removed"
	case f.Binary:
		return "binary"
	}
	if reason := rules.Reason(f.Path); reason != "" {
		return reason
	}
	if size, err := p.Workspace.Size(ctx, p.Workspace.HeadSHA, f.Path); err == nil && size > maxFileBytes {
		return "larger than 1 MB"
	}
	return ""
}

// scope matches rules to reviewable files and applies the fan-out cap.
func (p *Prepared) scope() {
	var exemplar []ScopedRule
	for _, r := range p.Effective.Rules {
		files := r.Patterns.Filter(p.Files)
		if len(files) == 0 {
			continue
		}
		switch r.Kind {
		case schema.KindExemplar:
			exemplar = append(exemplar, ScopedRule{Rule: r, Lane: LaneExemplar, Files: files})
		case schema.KindCompiled:
			p.Scoped = append(p.Scoped, ScopedRule{Rule: r, Lane: LaneCompiled, Files: files})
		case schema.KindBugClass:
			// Bug classes run in the bug lane (bugClasses), not here.
		}
	}
	// No precision history exists yet, so the cap orders by rule id.
	cands := make([]scope.Candidate, len(exemplar))
	byID := map[string]ScopedRule{}
	for i, s := range exemplar {
		cands[i] = scope.Candidate{RuleID: s.Rule.ID}
		byID[s.Rule.ID] = s
	}
	run, deferred := scope.ApplyFanOutCap(cands, p.Effective.Global.MaxExemplarRules)
	for _, c := range run {
		p.Scoped = append(p.Scoped, byID[c.RuleID])
	}
	for _, c := range deferred {
		p.Deferred = append(p.Deferred, byID[c.RuleID])
	}
}

type lineLoc struct {
	side scope.Side
	line int
}

// hunkLineMap maps 1-based line numbers in concatenated hunk text to the
// file side and line they show.
func hunkLineMap(text string) map[int]lineLoc {
	out := map[int]lineLoc{}
	var oldLine, newLine int
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for n := 1; sc.Scan(); n++ {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "@@"):
			oldLine, newLine = parseHunkHeader(l)
		case strings.HasPrefix(l, "+"):
			out[n] = lineLoc{scope.SideRight, newLine}
			newLine++
		case strings.HasPrefix(l, "-"):
			out[n] = lineLoc{scope.SideLeft, oldLine}
			oldLine++
		case strings.HasPrefix(l, " "):
			out[n] = lineLoc{scope.SideRight, newLine}
			oldLine++
			newLine++
		}
	}
	return out
}

func parseHunkHeader(h string) (oldStart, newStart int) {
	// "@@ -a,b +c,d @@ ..."
	fields := strings.Fields(h)
	if len(fields) < 3 {
		return 0, 0
	}
	parse := func(s string) int {
		s = strings.TrimLeft(s, "-+")
		n, _ := strconv.Atoi(strings.SplitN(s, ",", 2)[0])
		return n
	}
	return parse(fields[1]), parse(fields[2])
}

func secretFinding(path string, loc lineLoc, gitleaksRule string) Finding {
	return Finding{
		RuleID:   SecretRuleID,
		Lane:     LaneSecrets,
		Severity: schema.SeverityBlocking,
		Category: "Security",
		CCLabel:  ccLabel(schema.SeverityBlocking, "Security"),
		Anchor:   scope.Anchor{Path: path, Side: loc.side, StartLine: loc.line, EndLine: loc.line},
		Message: "This change appears to contain a secret (gitleaks rule `" + gitleaksRule + "`). " +
			"It was redacted before review. Remove it and rotate the credential if it is real.",
		State: StateCandidate,
	}
}

func parseGitleaksIgnore(text string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out[line] = true
		}
	}
	return out
}

func fileText(fsys interface {
	ReadFile(string) ([]byte, error)
}, name string) string {
	data, err := fsys.ReadFile(name)
	if err != nil {
		return ""
	}
	return string(data)
}

// ccLabel is the Conventional Comments label, attached by code.
func ccLabel(severity, category string) string {
	base := "suggestion"
	switch severity {
	case schema.SeverityBlocking:
		base = "issue (blocking)"
		return strings.TrimSuffix(base, ")") + ", " + strings.ToLower(category) + ")"
	case schema.SeverityImportant:
		base = "issue"
	}
	return base + " (" + strings.ToLower(category) + ")"
}
