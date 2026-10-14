package evaluate

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	ok := Request{Repo: "acme/app", BaseSHA: strings.Repeat("a", 40), Diff: "diff --git a/x b/x\n", Agent: "claude_code"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Request){
		"repo":        func(r *Request) { r.Repo = "app" },
		"short sha":   func(r *Request) { r.BaseSHA = "abc1234" },
		"upper sha":   func(r *Request) { r.BaseSHA = strings.Repeat("A", 40) },
		"empty diff":  func(r *Request) { r.Diff = "" },
		"not a diff":  func(r *Request) { r.Diff = "hello" },
		"huge diff":   func(r *Request) { r.Diff = "diff --git " + strings.Repeat("x", MaxDiff) },
		"agent chars": func(r *Request) { r.Agent = "Claude Code" },
	} {
		r := ok
		mut(&r)
		if r.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateEval(t *testing.T) {
	sha := strings.Repeat("a", 40)
	ok := Request{Repo: "acme/exemplar-config", BaseSHA: sha, Mode: ModeEval, HeadSHA: strings.Repeat("b", 40), Rules: []string{"no-raw-sql"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Request){
		"no head":       func(r *Request) { r.HeadSHA = "" },
		"a diff":        func(r *Request) { r.Diff = "diff --git a/x b/x\n" },
		"bad rule":      func(r *Request) { r.Rules = []string{"../etc"} },
		"too many":      func(r *Request) { r.Rules = make([]string, MaxRules+1) },
		"unknown mode":  func(r *Request) { r.Mode = "fix" },
		"head w/o eval": func(r *Request) { r.Mode = ""; r.Diff = "diff --git a/x b/x\n" },
	} {
		r := ok
		mut(&r)
		if r.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
