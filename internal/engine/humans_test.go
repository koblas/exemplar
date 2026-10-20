package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/schema"
)

func TestReviewHumanDedupe(t *testing.T) {
	reviewer := HumanComment{
		ID: 7, Author: "dana", AuthorType: "User", Association: "MEMBER", Path: "main.go", Side: "RIGHT",
		Start: httpGetLine, End: httpGetLine, Body: "This should use the request's context.",
	}
	with := func(mod func(*HumanComment)) HumanComment { c := reviewer; mod(&c); return c }
	tests := []struct {
		name      string
		comment   HumanComment
		judge     bool
		wantDup   bool
		wantJudge int
	}{
		{name: "reviewer, same concern", comment: reviewer, judge: true, wantDup: true, wantJudge: 1},
		{name: "reviewer, other concern", comment: reviewer, wantJudge: 1},
		{name: "collaborator", comment: with(func(c *HumanComment) { c.Association = "collaborator" }), judge: true, wantDup: true, wantJudge: 1},
		{name: "the PR's author", comment: with(func(c *HumanComment) { c.Author = "Alice" }), judge: true},
		{name: "a bot", comment: with(func(c *HumanComment) { c.AuthorType = "Bot" }), judge: true},
		{name: "an outside contributor", comment: with(func(c *HumanComment) { c.Association = "CONTRIBUTOR" }), judge: true},
		{name: "no association", comment: with(func(c *HumanComment) { c.Association = "NONE" }), judge: true},
		{name: "other side", comment: with(func(c *HumanComment) { c.Side = "LEFT" }), judge: true},
		{name: "other lines", comment: with(func(c *HumanComment) { c.Start, c.End = 2, 3 }), judge: true},
		{name: "a range that covers it", comment: with(func(c *HumanComment) { c.Start, c.End = 5, 12 }), judge: true, wantDup: true, wantJudge: 1},
		{name: "empty body", comment: with(func(c *HumanComment) { c.Body = "  " }), judge: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t, "Fetch inventory", "Adds a fetch.")
			e.prep.PR.Author = "alice"
			eng, tr := newEngine(e, Script{
				Generator: contextViolation(""), Quote: func(path string, line int) string { return headLineNoT(e.prep, path, line) },
				HumanJudge: func(pair string) bool {
					if !strings.Contains(pair, "Reviewer's comment") || !strings.Contains(pair, tt.comment.Body) {
						t.Errorf("judge context = %q", pair)
					}
					return tt.judge
				},
				LinterJudge: func(string) bool { t.Error("linter judge called"); return false },
			})
			res, err := eng.Review(t.Context(), e.prep, Options{Caller: "eval", HumanComments: func(context.Context) ([]HumanComment, error) {
				return []HumanComment{tt.comment}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			fs := findByRule(res.Findings, "propagate-context")
			if len(fs) != 1 {
				t.Fatalf("findings = %+v", res.Findings)
			}
			if dup := fs[0].State == StateDropped && fs[0].Suppressed == SuppressHumanDuplicate; dup != tt.wantDup {
				t.Errorf("finding = %s/%s, want duplicate %v", fs[0].State, fs[0].Suppressed, tt.wantDup)
			} else if !dup && fs[0].State != StatePosted {
				t.Errorf("finding = %s/%s, want posted", fs[0].State, fs[0].Suppressed)
			}
			n := 0
			for _, c := range tr.Calls {
				if c.Stage == llm.StageLight {
					n++
					if c.DataClasses != "diff,pr_text,review_comments" {
						t.Errorf("data classes = %q", c.DataClasses)
					}
				}
			}
			if n != tt.wantJudge || res.Humans == nil || res.Humans.Judged != tt.wantJudge {
				t.Errorf("judge calls = %d, trace = %+v", n, res.Humans)
			}
			if res.Stage1.Input.Candidates != 2 {
				t.Errorf("candidates = %d", res.Stage1.Input.Candidates)
			}
		})
	}
}

// A finding both a reviewer and a linter raised is the reviewer's: it
// counts as agreement.
func TestHumanDuplicateBeforeLinter(t *testing.T) {
	e := setup(t, "Fetch inventory", "Adds a fetch.")
	e.prep.PR.Author = "alice"
	e.prep.Effective.RepoConfig.Linters = []string{"golangci-lint"}
	for i := range e.prep.Effective.Rules {
		if e.prep.Effective.Rules[i].ID == "propagate-context" {
			e.prep.Effective.Rules[i].LinterMap = map[string][]string{"golangci-lint": {"noctx"}}
		}
	}
	eng, _ := newEngine(e, Script{
		Generator: contextViolation(""), Quote: func(path string, line int) string { return headLineNoT(e.prep, path, line) },
		HumanJudge: func(string) bool { return true },
	})
	res, err := eng.Review(t.Context(), e.prep, Options{
		Caller: "eval",
		HumanComments: func(context.Context) ([]HumanComment, error) {
			return []HumanComment{{ID: 7, Author: "dana", AuthorType: "User", Association: "OWNER", Path: "main.go", Side: "RIGHT", Start: httpGetLine, Body: "Use ctx here."}}, nil
		},
		Linters: func(context.Context) ([]LinterAnnotation, error) {
			return []LinterAnnotation{{Check: "golangci-lint", Path: "main.go", Start: httpGetLine, End: httpGetLine, Title: "noctx"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fs := findByRule(res.Findings, "propagate-context"); len(fs) != 1 || fs[0].Suppressed != SuppressHumanDuplicate {
		t.Errorf("findings = %+v", fs)
	}
}

// A reviewer comment is redacted before the judge sees it.
func TestHumanJudgeRedacts(t *testing.T) {
	e := setup(t, "Fetch inventory", "Adds a fetch.")
	e.prep.PR.Author = "alice"
	tok := fakeToken()
	var seen string
	eng, _ := newEngine(e, Script{
		Generator: contextViolation(""), Quote: func(path string, line int) string { return headLineNoT(e.prep, path, line) },
		HumanJudge: func(pair string) bool { seen = pair; return false },
	})
	_, err := eng.Review(t.Context(), e.prep, Options{Caller: "eval", HumanComments: func(context.Context) ([]HumanComment, error) {
		return []HumanComment{{
			ID: 7, Author: "dana", AuthorType: "User", Association: "MEMBER", Path: "main.go", Side: "RIGHT",
			Start: httpGetLine, Body: "Use ctx. Also the token is " + tok,
		}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if seen == "" || strings.Contains(seen, tok) {
		t.Errorf("judge context = %q", seen)
	}
}

// Security, blocking and secret findings are never judged duplicates of
// a reviewer's comment, as with linters: a vague comment from a
// collaborator must not take a blocking finding off the PR.
func TestHumanDedupeSkipsUnjudgeable(t *testing.T) {
	for name, mod := range map[string]func(*schema.Rule){
		"blocking": func(r *schema.Rule) { r.Severity = schema.SeverityBlocking },
		"security": func(r *schema.Rule) { r.Category = "Security" },
	} {
		t.Run(name, func(t *testing.T) {
			e := setup(t, "Fetch inventory", "Adds a fetch.")
			e.prep.PR.Author = "alice"
			for i := range e.prep.Effective.Rules {
				if e.prep.Effective.Rules[i].ID == "propagate-context" {
					mod(&e.prep.Effective.Rules[i].Rule)
				}
			}
			for i := range e.prep.Scoped {
				if e.prep.Scoped[i].Rule.ID == "propagate-context" {
					mod(&e.prep.Scoped[i].Rule.Rule)
				}
			}
			eng, _ := newEngine(e, Script{
				Generator: contextViolation(""), Quote: func(path string, line int) string { return headLineNoT(e.prep, path, line) },
				HumanJudge: func(string) bool { t.Error("human judge called"); return true },
			})
			res, err := eng.Review(t.Context(), e.prep, Options{Caller: "eval", HumanComments: func(context.Context) ([]HumanComment, error) {
				return []HumanComment{{
					ID: 7, Author: "dana", AuthorType: "User", Association: "MEMBER", Path: "main.go", Side: "RIGHT",
					Start: httpGetLine, End: httpGetLine, Body: "Looks fine to me.",
				}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if fs := findByRule(res.Findings, "propagate-context"); len(fs) != 1 || fs[0].Suppressed == SuppressHumanDuplicate {
				t.Errorf("findings = %+v", fs)
			}
		})
	}
}
