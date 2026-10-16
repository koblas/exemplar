package compile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/gitexec"
)

// Limits on what a case materializes.
const (
	maxCaseFiles = 20
	maxFileBytes = 1 << 20
)

// Case is one eval case to score a rule against.
type Case struct {
	ID     string
	Kind   string // positive | negative | injected
	Repo   string
	SHA    string
	Patch  string
	Expect []Expect
	// Files are the paths to scan; for positives and injected cases the
	// expected paths, for negatives the PR's in-language files.
	Files []string
}

// Expect is a finding the rule must produce, overlapping Lines.
type Expect struct {
	Path  string `json:"path"`
	Lines [2]int `json:"lines"`
}

// ParseRef splits "owner/name@sha".
func ParseRef(ref string) (string, string, error) {
	repo, sha, ok := strings.Cut(ref, "@")
	if !ok || strings.Count(repo, "/") != 1 || sha == "" {
		return "", "", fmt.Errorf("%w %q", ErrBadCaseRef, ref)
	}
	return repo, sha, nil
}

// DecodeExpect parses candidate_eval_cases.expect.
func DecodeExpect(data []byte) ([]Expect, error) {
	var e []Expect
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("compile: bad expect: %w", err)
	}
	return e, nil
}

// FileSource reads a file at a commit (evalgen.CheckoutFiles in production).
type FileSource interface {
	FileAt(ctx context.Context, repo, sha, path string) ([]byte, error)
}

// Outcome is how one case scored.
type Outcome struct {
	Case    string `json:"case"`
	Kind    string `json:"kind"`
	Result  string `json:"result"` // tp | fn | fp | tn | unavailable
	Matches int    `json:"matches"`
	Detail  string `json:"detail,omitempty"`
}

// Score is a rule's eval result at case level: a positive or injected
// case is a true positive when a match overlaps an expected range; a
// negative case with any match is a false positive.
type Score struct {
	Cases, TP, FP, FN, TN int
	Outcomes              []Outcome
}

// Precision is TP/(TP+FP), 0 when the rule found nothing.
func (s Score) Precision() float64 {
	if s.TP+s.FP == 0 {
		return 0
	}
	return float64(s.TP) / float64(s.TP+s.FP)
}

// Recall is TP/(TP+FN).
func (s Score) Recall() float64 {
	if s.TP+s.FN == 0 {
		return 0
	}
	return float64(s.TP) / float64(s.TP+s.FN)
}

// Engine runs rules (compiled.Runner).
type Engine interface {
	Run(ctx context.Context, dir, engine, ruleBody string, files []string) ([]compiled.Match, error)
	Validate(ctx context.Context, engine, ruleBody string) error
}

// Evaluate materializes each case under scratch and runs the rule on it.
// A case whose files cannot be read is reported unavailable and not
// scored; an engine failure fails the whole evaluation.
func Evaluate(ctx context.Context, eng Engine, files FileSource, scratch, engine, rule string, cases []Case) (Score, error) {
	var s Score
	for i, c := range cases {
		dir := filepath.Join(scratch, fmt.Sprintf("case-%03d", i))
		paths, err := materialize(ctx, files, dir, c)
		if err != nil || len(paths) == 0 {
			detail := "no files to scan"
			if err != nil {
				detail = err.Error()
			}
			s.Outcomes = append(s.Outcomes, Outcome{Case: c.ID, Kind: c.Kind, Result: "unavailable", Detail: detail})
			continue
		}
		matches, err := eng.Run(ctx, dir, engine, rule, paths)
		if err != nil {
			return s, fmt.Errorf("run case %s: %w", c.ID, err)
		}
		o := Outcome{Case: c.ID, Kind: c.Kind, Matches: len(matches)}
		s.Cases++
		switch {
		case c.Kind == "negative" && len(matches) > 0:
			o.Result, s.FP = "fp", s.FP+1
		case c.Kind == "negative":
			o.Result, s.TN = "tn", s.TN+1
		case overlaps(matches, c.Expect):
			o.Result, s.TP = "tp", s.TP+1
		default:
			o.Result, s.FN = "fn", s.FN+1
		}
		s.Outcomes = append(s.Outcomes, o)
	}
	return s, nil
}

func overlaps(ms []compiled.Match, expect []Expect) bool {
	for _, m := range ms {
		for _, e := range expect {
			if m.Path == e.Path && m.StartLine <= e.Lines[1] && m.EndLine >= e.Lines[0] {
				return true
			}
		}
	}
	return false
}

// materialize writes the case's files into dir and returns their paths.
func materialize(ctx context.Context, src FileSource, dir string, c Case) ([]string, error) {
	paths := c.Files
	if len(paths) > maxCaseFiles {
		paths = paths[:maxCaseFiles]
	}
	var out []string
	for _, p := range paths {
		if !safePath(p) {
			continue
		}
		data, err := src.FileAt(ctx, c.Repo, c.SHA, p)
		if err != nil {
			if c.Kind == "negative" {
				continue // a file deleted by the PR, say
			}
			return nil, fmt.Errorf("read %s@%s:%s: %w", c.Repo, c.SHA, p, err)
		}
		if len(data) > maxFileBytes {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return nil, fmt.Errorf("create case dir: %w", err)
		}
		if err := os.WriteFile(full, data, 0o600); err != nil {
			return nil, fmt.Errorf("write case file: %w", err)
		}
		out = append(out, p)
	}
	if c.Kind == "injected" {
		if c.Patch == "" {
			return nil, errInjectedNoPatch
		}
		pf := filepath.Join(dir, ".exemplar.patch")
		if err := os.WriteFile(pf, []byte(c.Patch), 0o600); err != nil {
			return nil, fmt.Errorf("write case patch: %w", err)
		}
		// --unsafe-paths is not set, so the patch cannot write outside dir.
		// The ceiling keeps git from treating an enclosing repo as the target.
		env := append(gitexec.Env(""), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
		if _, err := gitexec.Run(ctx, dir, env, "apply", "--", pf); err != nil {
			return nil, fmt.Errorf("apply patch: %w", err)
		}
		_ = os.Remove(pf)
	}
	return out, nil
}

func safePath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "-") && path.Clean(p) == p &&
		p != ".." && !strings.HasPrefix(p, "../") && !strings.Contains(p, "\x00")
}

// inLanguage keeps files whose extension belongs to lang.
func inLanguage(files []string, lang Language) []string {
	var out []string
	for _, f := range files {
		if slices.Contains(lang.Exts, strings.ToLower(path.Ext(f))) {
			out = append(out, f)
		}
	}
	return out
}
