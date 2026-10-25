package pipeline

import (
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/feedbacklink"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
)

func TestCommentBodySuggestion(t *testing.T) {
	yes, no := true, false
	f := engine.Finding{RuleID: "propagate-context", CCLabel: "issue", Message: "http.Get ignores ctx.", Fix: "\treq, err := newReq(ctx)\r\n"}
	tests := []struct {
		name  string
		valid *bool
		want  bool
	}{
		{"valid", &yes, true},
		{"invalid", &no, false},
		{"not validated", nil, false},
	}
	for _, tt := range tests {
		f.FixValid = tt.valid
		body := commentBody(f, &Record{}, "")
		has := strings.Contains(body, "```suggestion\n\treq, err := newReq(ctx)\n```")
		if has != tt.want || !strings.HasPrefix(body, "**issue:** http.Get ignores ctx.") || !strings.Contains(body, "reply `/exemplar fixed`") {
			t.Errorf("%s: body = %q", tt.name, body)
		}
	}
}

// The message is model output: it may not open a fence (a suggestion
// would skip fix validation), mention anyone, or forge the rule footer.
func TestCommentBodyModelMessage(t *testing.T) {
	msg := "Bad.\n\n```suggestion\nos.Exit(0)\n```\n~~~suggestion\nx\n~~~\ncc @acme/security\n<sub>Rule `other-rule`</sub> <!-- hidden --> keep `List<T>` and `a``b`"
	f := engine.Finding{RuleID: "propagate-context", CCLabel: "issue", Message: msg}
	body := commentBody(f, &Record{}, "")
	for _, bad := range []string{"```", "~~~", "@acme", "<sub>Rule `other-rule`", "<!--"} {
		if strings.Contains(body, bad) {
			t.Errorf("body contains %q:\n%s", bad, body)
		}
	}
	if strings.Count(body, "<sub>") != 1 || !strings.Contains(body, "os.Exit(0)") || !strings.Contains(body, "List") {
		t.Errorf("body = %s", body)
	}
}

func TestCommentBodyLinks(t *testing.T) {
	links, err := feedbacklink.New([]byte(strings.Repeat("k", 32)), "https://exemplar.example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	f := engine.Finding{ID: 8812, RuleID: "propagate-context", CCLabel: "issue", Message: "m"}
	body := commentBody(f, &Record{Links: links}, "")
	if !strings.Contains(body, "[Fixed](https://exemplar.example.invalid/f/8812/fixed?s=") || !strings.Contains(body, "or reply `/exemplar <reason>`") {
		t.Errorf("body = %s", body)
	}
	// Not persisted (no id): no links to sign.
	f.ID = 0
	if body := commentBody(f, &Record{Links: links}, ""); strings.Contains(body, "/f/") {
		t.Errorf("unpersisted body = %s", body)
	}
}

// Findings the verifier quorum held back are named in the check run.
func TestCheckTextQuorum(t *testing.T) {
	t.Parallel()
	res := &engine.Result{Findings: []engine.Finding{
		{Suppressed: engine.SuppressQuorum}, {Suppressed: engine.SuppressQuorumShed}, {Suppressed: engine.SuppressQuorumShed},
	}}
	text := checkText(&engine.Prepared{}, res)
	if !strings.Contains(text, "1 finding(s) held back: fewer than the verifier quorum") || !strings.Contains(text, "2 finding(s) held back: the verifier quorum was shed") {
		t.Errorf("check text = %s", text)
	}
}

func TestScopeNote(t *testing.T) {
	t.Parallel()
	withExtra := &engine.Prepared{Effective: &config.Effective{RepoConfig: schema.RepoConfig{
		Context: &schema.RepoContext{ExtraRepos: []string{"acme/lib", "acme/sdk"}},
	}}}
	plain := &engine.Prepared{Effective: &config.Effective{}}
	for name, tc := range map[string]struct {
		p    *engine.Prepared
		res  *engine.Result
		want string
	}{
		"search configured": {withExtra, &engine.Result{}, ""},
		"search off":        {plain, &engine.Result{SearchOff: true}, "\n\nCross-repository callers were not checked (search is not configured)."},
		"search off, extra repos": {
			withExtra, &engine.Result{SearchOff: true},
			"\n\nCross-repository callers were not checked (search is not configured). Not searched: acme/lib, acme/sdk.",
		},
	} {
		if got := scopeNote(tc.p, tc.res); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// A grouped comment lists every other occurrence and drops the
// suggestion, which cannot fix them all.
func TestCommentBodyGroup(t *testing.T) {
	t.Parallel()
	yes := true
	f := engine.Finding{
		RuleID: "propagate-context", CCLabel: "issue", Message: "m", Fix: "x", FixValid: &yes,
		Members: []scope.Anchor{{Path: "a.go", StartLine: 4, EndLine: 4}, {Path: "b/c.go", StartLine: 7, EndLine: 9}},
	}
	body := commentBody(f, &Record{LinkBase: "https://github.com/acme/api"}, "abc123")
	for _, want := range []string{
		"Also in 2 other place(s)",
		"- [a.go:4](https://github.com/acme/api/blob/abc123/a.go#L4)",
		"- [b/c.go:7-9](https://github.com/acme/api/blob/abc123/b/c.go#L7-L9)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "```suggestion") {
		t.Errorf("grouped body has a suggestion:\n%s", body)
	}
	if body := commentBody(f, &Record{}, ""); !strings.Contains(body, "- `a.go:4`") {
		t.Errorf("no link base: %s", body)
	}
}
