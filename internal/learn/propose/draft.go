// Package propose is the config writer (spec §8.7): it turns the top
// candidates into draft PRs in exemplar-config, each adding one rule with
// status shadow, for the candidate's owner to edit, promote or kill.
// Nothing is enforced until an owner merges.
package propose

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/goccy/go-yaml"

	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
)

// Candidate is what the drafter knows about one candidate.
type Candidate struct {
	ID                int64
	Ext               string // cluster partition: ".go", or "*" for language-agnostic
	Title             string
	Description       string
	ClusterSize       int
	DistinctReviewers int
	Score             float64
	ScoreDetail       map[string]any
	Owner             string
	Sources           []Source
	Incidents         []Incident
	EvalCases         []EvalCase
	// Compiled is the latest accepted compile attempt (spec §8.6), if any;
	// the draft is then kind: compiled.
	Compiled *Compiled
}

// Source is one source comment, as links and labels only. Comment bodies
// never go into exemplar-config, which is readable by more people than many
// of the repos comments are mined from.
type Source struct {
	CommentURL        string
	Repo              string
	Path              string
	DocURLs           []string
	Kind              string // human | accepted_bot_finding | revert_seed
	Category          string
	PreferredApproach string
}

// EvalCase is a generated eval case (spec §8.8) to ship with the rule.
type EvalCase struct {
	ID        string
	Kind      string // positive | negative | injected
	Name      string
	Ref       string // repo@sha
	Patch     string // injected only
	Expect    []schema.Expectation
	SpotCheck bool
	// Paths are the files the case runs on, for negatives.
	Paths []string
}

// Compiled is an accepted compile attempt and its scores.
type Compiled struct {
	AttemptID         int64
	Engine, Rule      string
	Precision, Recall float64
	Cases             int
	// Baseline is what the scores were held to: "exemplar" (its own eval
	// scores) or "thresholds" (global.yaml eval_thresholds).
	Baseline                          string
	BaselinePrecision, BaselineRecall float64
}

// Incident is a path event linked to a source path.
type Incident struct {
	Repo, Path, Kind, Ref string
}

// Draft is a rule file and the PR that proposes it.
type Draft struct {
	CandidateID int64
	RuleID      string
	Path        string
	Branch      string
	Rule        []byte
	Title       string
	Body        string
	// Files are extra files the PR adds (eval cases and their patches).
	Files map[string][]byte
}

// Defaults for drafted rules. The owner adjusts severity and scope in
// review; shadow status means the rule runs and is stored but never posts.
const (
	draftSeverity    = schema.SeveritySuggestion
	fallbackCategory = "Maintainability"
	fallbackOwner    = "@platform/exemplar"
	maxCitedComments = 20
	maxListedSources = 50
	reviewByMonths   = 12
)

var ownerRE = regexp.MustCompile(`^@[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)?$`)

// ruleYAML fixes the field order of drafted rule files.
type ruleYAML struct {
	ID                string        `yaml:"id"`
	Kind              string        `yaml:"kind"`
	Category          string        `yaml:"category"`
	Severity          string        `yaml:"severity"`
	Owner             string        `yaml:"owner"`
	Status            string        `yaml:"status"`
	ReviewBy          string        `yaml:"review_by"`
	FilePatterns      []string      `yaml:"file_patterns"`
	Description       string        `yaml:"description"`
	PreferredApproach string        `yaml:"preferred_approach,omitempty"`
	Matcher           *matcherYAML  `yaml:"matcher,omitempty"`
	Citation          *citationYAML `yaml:"citation,omitempty"`
	EvalCases         string        `yaml:"eval_cases,omitempty"`
}

type matcherYAML struct {
	Engine string `yaml:"engine"`
	Rule   string `yaml:"rule"`
}

type citationYAML struct {
	Doc      string   `yaml:"doc,omitempty"`
	Comments []string `yaml:"comments,omitempty"`
}

// BuildDraft renders the rule and PR for a candidate and validates the rule
// against schema/rule.schema.json, so a malformed draft never reaches
// GitHub.
func BuildDraft(c Candidate, now time.Time) (*Draft, error) {
	if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.Description) == "" {
		return nil, errors.New("propose: candidate has no title or description")
	}
	ruleID := RuleID(c.Title, c.ID)
	category := category(c.Sources)
	owner := c.Owner
	if !ownerRE.MatchString(owner) {
		owner = fallbackOwner // e.g. an email-address CODEOWNERS entry
	}
	r := ruleYAML{
		ID: ruleID, Kind: schema.KindExemplar, Category: category, Severity: draftSeverity,
		Owner: owner, Status: schema.StatusShadow,
		ReviewBy:          now.AddDate(0, reviewByMonths, 0).Format(time.DateOnly),
		FilePatterns:      filePatterns(c.Ext),
		Description:       strings.TrimSpace(c.Description) + "\n",
		PreferredApproach: preferredApproach(c.Sources),
	}
	if cc := c.Compiled; cc != nil {
		r.Kind, r.Matcher = schema.KindCompiled, &matcherYAML{Engine: cc.Engine, Rule: cc.Rule}
	}
	if cite := citation(c.Sources); cite != nil {
		r.Citation = cite
	}
	files, err := evalFiles(ruleID, c.EvalCases)
	if err != nil {
		return nil, err
	}
	if len(files) > 0 {
		r.EvalCases = "evals/" + ruleID + "/"
	}
	data, err := yaml.MarshalWithOptions(r, yaml.UseLiteralStyleIfMultiline(true))
	if err != nil {
		return nil, fmt.Errorf("propose: render rule: %w", err)
	}
	header := fmt.Sprintf("# Proposed by Exemplar from candidate %d (spec §8.7). Status shadow: it runs and\n# is stored but posts nothing until an owner sets status: active.\n", c.ID)
	data = append([]byte(header), data...)

	var check schema.Rule
	if err := schema.Decode(schema.DocRule, data, &check); err != nil {
		return nil, fmt.Errorf("propose: drafted rule is invalid: %w", err)
	}
	if _, err := scope.CompilePatterns(check.FilePatterns); err != nil {
		return nil, fmt.Errorf("propose: drafted file_patterns: %w", err)
	}
	return &Draft{
		CandidateID: c.ID,
		RuleID:      ruleID,
		Path:        fmt.Sprintf("rules/%s/%s.yaml", strings.ToLower(category), ruleID),
		Branch:      fmt.Sprintf("exemplar/candidate-%d", c.ID),
		Rule:        data,
		Title:       "Candidate rule: " + strings.TrimSpace(c.Title),
		Body:        body(c, ruleID, owner),
		Files:       files,
	}, nil
}

// RuleID is a slug of the title plus the candidate id, so it is unique and
// stays the same when the candidate is re-drafted.
func RuleID(title string, candidateID int64) string {
	suffix := fmt.Sprintf("-c%d", candidateID)
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if limit := 80 - len(suffix); len(slug) > limit {
		slug = strings.TrimRight(slug[:limit], "-")
	}
	if slug == "" {
		slug = "rule"
	}
	return slug + suffix
}

func filePatterns(ext string) []string {
	if ext == "" || ext == "*" {
		return []string{"**/*"}
	}
	return []string{"**/*" + ext}
}

// category is the most common extractor category among the sources,
// normalized to one capitalized word (it names the rules/ directory).
func category(sources []Source) string {
	counts := map[string]int{}
	for _, s := range sources {
		if c := normalizeCategory(s.Category); c != "" {
			counts[c]++
		}
	}
	best, n := fallbackCategory, 0
	for c, k := range counts {
		if k > n || (k == n && c < best) {
			best, n = c, k
		}
	}
	return best
}

func normalizeCategory(c string) string {
	word := strings.FieldsFunc(c, func(r rune) bool { return !unicode.IsLetter(r) })
	if len(word) == 0 || len(word[0]) > 40 {
		return ""
	}
	w := strings.ToLower(word[0])
	return strings.ToUpper(w[:1]) + w[1:]
}

func preferredApproach(sources []Source) string {
	counts := map[string]int{}
	for _, s := range sources {
		if p := strings.TrimSpace(s.PreferredApproach); p != "" {
			counts[p]++
		}
	}
	best, n := "", 0
	for p, k := range counts {
		if k > n || (k == n && p < best) {
			best, n = p, k
		}
	}
	if best == "" {
		return ""
	}
	return best + "\n"
}

func citation(sources []Source) *citationYAML {
	var docs, comments []string
	for _, s := range sources {
		docs = append(docs, s.DocURLs...)
		if s.CommentURL != "" {
			comments = append(comments, s.CommentURL)
		}
	}
	slices.Sort(docs)
	docs = slices.Compact(docs)
	slices.Sort(comments)
	if len(comments) > maxCitedComments {
		comments = comments[:maxCitedComments]
	}
	if len(docs) == 0 && len(comments) == 0 {
		return nil
	}
	c := &citationYAML{Comments: comments}
	if len(docs) > 0 {
		c.Doc = docs[0]
	}
	return c
}

func body(c Candidate, ruleID, owner string) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Exemplar mined this constraint from review history and proposes it as rule `%s`, in **shadow**: it will run on PRs and be measured, but post nothing until you set `status: active`.\n\n", ruleID)
	fmt.Fprintf(&b, "> **%s**\n>\n> %s\n\n", strings.TrimSpace(c.Title), strings.ReplaceAll(strings.TrimSpace(c.Description), "\n", "\n> "))

	b.WriteString("| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Owner | %s |\n| Source comments | %d |\n| Distinct reviewers | %d |\n| Score | %.2f |\n", owner, c.ClusterSize, c.DistinctReviewers, c.Score)
	if f, ok := c.ScoreDetail["factors"].(map[string]any); ok {
		keys := make([]string, 0, len(f))
		for k := range f {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %v", k, formatFactor(f[k])))
		}
		fmt.Fprintf(&b, "| Score factors | %s |\n", strings.Join(parts, ", "))
	}
	if cc := c.Compiled; cc != nil {
		writeCompiled(&b, cc)
	}
	b.WriteString("\n### Sources\n\n")
	sources := slices.Clone(c.Sources)
	slices.SortFunc(sources, func(a, b Source) int { return cmp.Compare(a.CommentURL, b.CommentURL) })
	for i, s := range sources {
		if i == maxListedSources {
			fmt.Fprintf(&b, "- … and %d more\n", len(sources)-maxListedSources)
			break
		}
		label := ""
		switch s.Kind {
		case "accepted_bot_finding":
			label = " (accepted bot finding)"
		case "revert_seed":
			label = " (revert)"
		}
		fmt.Fprintf(&b, "- [%s `%s`](%s)%s\n", s.Repo, s.Path, s.CommentURL, label)
	}
	var docs []string
	for _, s := range c.Sources {
		docs = append(docs, s.DocURLs...)
	}
	slices.Sort(docs)
	if docs = slices.Compact(docs); len(docs) > 0 {
		b.WriteString("\n### Linked documentation\n\n")
		for _, d := range docs {
			fmt.Fprintf(&b, "- %s\n", d)
		}
	}
	if len(c.Incidents) > 0 {
		b.WriteString("\n### Incidents and reverts on these paths\n\n")
		for _, inc := range c.Incidents {
			fmt.Fprintf(&b, "- %s `%s` in %s `%s`\n", inc.Kind, inc.Ref, inc.Repo, inc.Path)
		}
	}
	if len(c.EvalCases) > 0 {
		counts := map[string]int{}
		var spot []string
		for _, e := range c.EvalCases {
			counts[e.Kind]++
			if e.SpotCheck {
				spot = append(spot, "`"+e.ID+"` ("+e.Name+")")
			}
		}
		fmt.Fprintf(&b, "\n### Eval cases\n\n`evals/%s/cases.yaml`: %d from source comments (expect a finding), %d from approved PRs with no such comment (expect none), %d injected violations.\n", ruleID, counts["positive"], counts["negative"], counts["injected"])
		if len(spot) > 0 {
			b.WriteString("\nSpot-check these synthetic cases (spec §8.8):\n")
			for _, s := range spot {
				b.WriteString("- [ ] " + s + "\n")
			}
		}
	}
	b.WriteString("\n### Before you promote\n\n")
	b.WriteString("- [ ] Check the description says what the rule requires and why.\n")
	b.WriteString("- [ ] Set `severity` (drafted as `suggestion`) and narrow `file_patterns` (drafted from the source language).\n")
	if len(c.EvalCases) == 0 {
		b.WriteString("- [ ] Add eval cases under `evals/" + ruleID + "/`: none could be generated (no source comment records its commit and lines).\n")
	} else {
		b.WriteString("- [ ] Add cases until the eval check passes (it needs the minimum case count in global.yaml).\n")
	}
	b.WriteString("- [ ] To promote: set `status: active` and merge. To kill: close this PR with the `killed` label and a reason; the reason teaches the extractor.\n")
	return b.String()
}

func formatFactor(v any) string {
	if f, ok := v.(float64); ok {
		return fmt.Sprintf("%.2f", f)
	}
	return fmt.Sprint(v)
}

// evalCasesYAML is evals/<rule-id>/cases.yaml.
type evalCasesYAML struct {
	Rule  string         `yaml:"rule"`
	Cases []evalCaseYAML `yaml:"cases"`
}

type evalCaseYAML struct {
	Name      string           `yaml:"name"`
	Ref       string           `yaml:"ref"`
	Synthetic bool             `yaml:"synthetic,omitempty"`
	Patch     string           `yaml:"patch,omitempty"`
	Paths     []string         `yaml:"paths,omitempty,flow"`
	Expect    []evalExpectYAML `yaml:"expect"`
}

type evalExpectYAML struct {
	Path  string `yaml:"path"`
	Lines []int  `yaml:"lines,flow"`
}

// evalFiles renders the generated cases as evals/<rule-id>/cases.yaml plus
// one patch file per injected case, validated against
// schema/eval-cases.schema.json.
func evalFiles(ruleID string, cases []EvalCase) (map[string][]byte, error) {
	if len(cases) == 0 {
		return nil, nil
	}
	dir := "evals/" + ruleID + "/"
	files := map[string][]byte{}
	doc := evalCasesYAML{Rule: ruleID}
	for _, c := range cases {
		ec := evalCaseYAML{Name: c.Name, Ref: c.Ref, Paths: c.Paths, Expect: []evalExpectYAML{}}
		if c.Kind == "injected" {
			ec.Synthetic = true
			ec.Patch = "patches/" + c.ID + ".diff"
			files[dir+ec.Patch] = []byte(c.Patch)
		}
		for _, e := range c.Expect {
			ec.Expect = append(ec.Expect, evalExpectYAML{Path: e.Path, Lines: e.Lines})
		}
		doc.Cases = append(doc.Cases, ec)
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("propose: render eval cases: %w", err)
	}
	header := []byte("# Generated by Exemplar (spec §8.8). Cases reference code by repo@sha; synthetic\n# cases add a small patch. Spot-check the synthetic cases listed in the PR.\n")
	data = append(header, data...)
	var check schema.EvalCases
	if err := schema.Decode(schema.DocEvalCases, data, &check); err != nil {
		return nil, fmt.Errorf("propose: generated eval cases are invalid: %w", err)
	}
	files[dir+"cases.yaml"] = data
	return files, nil
}

// writeCompiled shows the compiled rule's scores beside the exemplar's
// (PRD §17.5). Exemplar scores come from eval CI; until it has run they
// are not measured and the bar shown is eval_thresholds.
func writeCompiled(b *bytes.Buffer, cc *Compiled) {
	fmt.Fprintf(b, "\n### Compiled to %s\n\n", cc.Engine)
	b.WriteString("Exemplar compiled this rule to a deterministic matcher, so it runs as Lane 1: no model call per PR. The rule file above is `kind: compiled`; drop `matcher` and set `kind: exemplar` to keep the model-based rule instead.\n\n")
	exemplar, bar := "not measured yet", "eval_thresholds"
	if cc.Baseline == "exemplar" {
		exemplar, bar = "", "the exemplar's scores"
	}
	b.WriteString("| | Compiled | Exemplar | Bar |\n|---|---|---|---|\n")
	ex := func(v float64) string {
		if exemplar != "" {
			return exemplar
		}
		return fmt.Sprintf("%.2f", v)
	}
	recallBar := cc.BaselineRecall
	if cc.Baseline == "exemplar" {
		recallBar *= 0.9
	}
	fmt.Fprintf(b, "| Precision | %.2f | %s | ≥ %.2f |\n", cc.Precision, ex(cc.BaselinePrecision), cc.BaselinePrecision)
	fmt.Fprintf(b, "| Recall | %.2f | %s | ≥ %.2f |\n", cc.Recall, ex(cc.BaselineRecall), recallBar)
	fmt.Fprintf(b, "\nScored over %d eval cases; held to %s. Compile attempt %d.\n", cc.Cases, bar, cc.AttemptID)
}
