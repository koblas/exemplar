package playground

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/synth"
)

const exampleConfig = "../../examples/exemplar-config"

const sandboxRepo = "exemplar-sandbox/playground"

// The draft: a candidate rule in shadow, as propose writes it.
const draftRule = `id: no-bare-http-get-c9
kind: exemplar
category: Reliability
severity: suggestion
owner: "@platform/backend"
status: shadow
review_by: 2027-10-01
file_patterns: ["**/*.go"]
description: |
  Outbound HTTP calls need a timeout; http.Get uses the default client, which has none.
`

func fakeToken() string {
	sum := sha256.Sum256([]byte("playground test token"))
	return "ghp_" + hex.EncodeToString(sum[:])[:36]
}

func scanner(t *testing.T) *redact.Scanner {
	t.Helper()
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func baseFS(t *testing.T) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(os.DirFS(exampleConfig), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(os.DirFS(exampleConfig), p)
		out[p] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParseTry(t *testing.T) {
	tests := []struct {
		body  string
		want  Target
		found bool
		err   bool
	}{
		{"/exemplar try acme/app#1234", Target{"acme/app", 1234}, true, false},
		{"Looks good.\n\n/exemplar try https://github.com/acme/app/pull/7\n", Target{"acme/app", 7}, true, false},
		{"  /exemplar try acme/app#12  ", Target{"acme/app", 12}, true, false},
		{"/exemplar try", Target{}, true, true},
		{"/exemplar try acme/app", Target{}, true, true},
		{"/exemplar try acme/app#0", Target{}, true, true},
		{"/exemplar try acme/app#1; rm -rf /", Target{}, true, true},
		{"please /exemplar try acme/app#1 later", Target{}, false, false}, // must start a line
		{"no command here", Target{}, false, false},
		{"/exemplar eval", Target{}, false, false},
	}
	for _, tt := range tests {
		got, found, err := ParseTry(tt.body)
		if got != tt.want || found != tt.found || (err != nil) != tt.err {
			t.Errorf("ParseTry(%q) = %v, %v, %v", tt.body, got, found, err)
		}
	}
}

func TestSafeMessage(t *testing.T) {
	sc := scanner(t)
	tests := map[string]string{
		"Use `http.Client` with a Timeout.":                            "Use `http.Client` with a Timeout.",
		"Replace `resp, err := http.Get(url)` with a client call.":     "Replace (code omitted) with a client call.",
		"Do this:\n```go\nclient := &http.Client{Timeout: t}\n```\nok": "Do this: (code omitted) ok",
		"cc @platform/backend":                                         "cc @\u200bplatform/backend",
		"token " + fakeToken() + " leaked":                             "token " + redact.Placeholder + " leaked",
		strings.Repeat("x", 400):                                       strings.Repeat("x", maxMessage) + "…",
	}
	for in, want := range tests {
		if got := SafeMessage(sc, in); got != want {
			t.Errorf("SafeMessage(%.40q) = %q, want %q", in, got, want)
		}
	}
}

func TestDraftSnapshot(t *testing.T) {
	base := baseFS(t)
	head := baseFS(t)
	head["rules/reliability/no-bare-http-get-c9.yaml"] = &fstest.MapFile{Data: []byte(draftRule)}
	delete(head, "rules/security/no-raw-sql-concat.yaml")
	// A PR that tries to widen its reach: these changes must not apply.
	head["platform/eligibility.yaml"] = &fstest.MapFile{Data: []byte("denylist: [{repo: other/x, reason: r}]\n")}
	head["repos/acme/new.yaml"] = &fstest.MapFile{Data: []byte("enabled: true\n")}
	changed := []string{"rules/reliability/no-bare-http-get-c9.yaml", "rules/security/no-raw-sql-concat.yaml",
		"platform/eligibility.yaml", "repos/acme/new.yaml", "README.md"}

	snap, ids, err := DraftSnapshot(base, head, changed, "head1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "no-bare-http-get-c9" {
		t.Errorf("ids = %v", ids)
	}
	var have []string
	for _, r := range snap.Rules {
		have = append(have, r.ID)
	}
	if strings.Contains(strings.Join(have, ","), "no-raw-sql-concat") {
		t.Errorf("deleted rule still loaded: %v", have)
	}
	if _, ok := snap.Repos["acme/new"]; ok || len(snap.Eligibility.Denylist) != 0 {
		t.Error("non-rule changes from the PR head were applied")
	}

	if _, _, err := DraftSnapshot(base, baseFS(t), []string{"README.md"}, "x"); !errors.Is(err, ErrNoDraftRules) {
		t.Errorf("no rule changes err = %v", err)
	}
	bad := baseFS(t)
	bad["rules/reliability/broken.yaml"] = &fstest.MapFile{Data: []byte("id: broken\n")}
	var probs config.Problems
	if _, _, err := DraftSnapshot(base, bad, []string{"rules/reliability/broken.yaml"}, "x"); !errors.As(err, &probs) {
		t.Errorf("invalid draft err = %v, want Problems", err)
	}
}

func TestRuleFileSnapshot(t *testing.T) {
	snap, id, err := RuleFileSnapshot(os.DirFS(exampleConfig), []byte(draftRule), "cli")
	if err != nil || id != "no-bare-http-get-c9" {
		t.Fatalf("RuleFileSnapshot = %v, %v", id, err)
	}
	found := false
	for _, r := range snap.Rules {
		if r.ID == id && r.Path == "rules/reliability/no-bare-http-get-c9.yaml" {
			found = true
		}
	}
	if !found {
		t.Error("draft rule not loaded where its category puts it")
	}
	// A rule with an existing id replaces it rather than colliding.
	existing, _ := os.ReadFile(exampleConfig + "/rules/reliability/propagate-context.yaml")
	if _, _, err := RuleFileSnapshot(os.DirFS(exampleConfig), existing, "cli"); err != nil {
		t.Errorf("replacing an existing rule: %v", err)
	}
}

// fakeGitHub answers for the target repo.
type fakeGitHub struct {
	readers map[string]bool
	pr      gh.PullRequest
	err     error
}

func (f *fakeGitHub) CanRead(_ context.Context, actor, repo string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.readers[actor] && repo == f.pr.Repo, nil
}

func (f *fakeGitHub) PullRequest(_ context.Context, repo string, n int) (gh.PullRequest, error) {
	if repo != f.pr.Repo || n != f.pr.Number {
		return gh.PullRequest{}, gh.ErrNotFound
	}
	return f.pr, nil
}

func (f *fakeGitHub) CloneToken(context.Context, string) (string, error) { return "", nil }

const handler = "package app\n\nimport \"net/http\"\n\nfunc Load(url string) error {\n\t_, err := http.Get(url)\n\treturn err\n}\n"

type fixture struct {
	runner *Runner
	tr     *engine.ScriptedTransport
	gh     *fakeGitHub
	snap   *config.Snapshot
}

func setup(t *testing.T) fixture {
	t.Helper()
	repo, err := synth.Build(t.Context(), t.TempDir(), map[string]string{"app/load.go": "package app\n"}, map[string]string{"app/load.go": handler}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap, _, err := RuleFileSnapshot(os.DirFS(exampleConfig), []byte(draftRule), "cli")
	if err != nil {
		t.Fatal(err)
	}
	sc := scanner(t)
	tr := &engine.ScriptedTransport{Script: engine.Script{
		Generator: func(task string, files []string) ([]string, string) {
			if strings.Contains(task, "no-bare-http-get-c9") || strings.Contains(task, "default client") {
				return files, `{"violations": [{"path": "app/load.go", "side": "RIGHT", "start_line": 6, "end_line": 6, ` +
					`"explanation": "Replace ` + "`_, err := http.Get(url)`" + ` with a client that has a Timeout; cc @platform/backend."}]}`
			}
			return files, `{"violations": []}`
		},
		Quote: func(string, int) string { return `_, err := http.Get(url)` },
	}}
	h := engine.NewHarness(engine.HarnessConfig{GatewayURL: "http://scripted.invalid", GatewayToken: "t", HTTPClient: &http.Client{Transport: tr}})
	fg := &fakeGitHub{readers: map[string]bool{"alice": true},
		pr: gh.PullRequest{Repo: sandboxRepo, Number: 41, Title: "Load things", Author: "bob", BaseSHA: repo.BaseSHA, HeadSHA: repo.HeadSHA}}
	return fixture{
		runner: &Runner{Engine: engine.New(h, sc, compiled.Runner{ASTGrep: "ast-grep-not-installed"}), Scanner: sc, GitHub: fg,
			WorkDir: t.TempDir(), CloneURL: func(string) string { return repo.Dir }, Caller: "playground"},
		tr: tr, gh: fg, snap: snap,
	}
}

func TestTry(t *testing.T) {
	f := setup(t)
	out, err := f.runner.Try(t.Context(), Request{Actor: "alice", Snapshot: f.snap, RuleIDs: []string{"no-bare-http-get-c9"}, Target: Target{sandboxRepo, 41}})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Ran || out.Denied || len(out.Findings) != 1 || !out.Findings[0].Kept || out.Findings[0].Start != 6 {
		t.Fatalf("outcome = %+v", out)
	}
	for _, want := range []string{"exemplar-sandbox/playground#41", "`app/load.go` L6", "(code omitted)", "@\u200bplatform/backend", "1 finding would be reported"} {
		if !strings.Contains(out.Reply, want) {
			t.Errorf("reply lacks %q:\n%s", want, out.Reply)
		}
	}
	if strings.Contains(out.Reply, "http.Get(url)") {
		t.Errorf("reply quotes code:\n%s", out.Reply)
	}
	// Only the draft ran: no other rule's agent and no baseline.
	for _, c := range f.tr.Calls {
		if c.Stage == "generator" && !strings.Contains(string(c.Body), "no-bare-http-get-c9") && !strings.Contains(string(c.Body), "default client") {
			t.Errorf("a rule other than the draft ran")
		}
	}
}

func TestTryDenials(t *testing.T) {
	f := setup(t)
	same := "no access to "
	cases := map[string]Request{
		"cannot read":  {Actor: "mallory", Snapshot: f.snap, RuleIDs: []string{"no-bare-http-get-c9"}, Target: Target{sandboxRepo, 41}},
		"not opted in": {Actor: "alice", Snapshot: f.snap, RuleIDs: []string{"no-bare-http-get-c9"}, Target: Target{"acme/other", 1}},
		"no snapshot":  {Actor: "alice", RuleIDs: []string{"no-bare-http-get-c9"}, Target: Target{sandboxRepo, 41}},
	}
	for name, req := range cases {
		out, err := f.runner.Try(t.Context(), req)
		if err != nil || !out.Denied || out.Ran || !strings.HasPrefix(out.Reply, same) {
			t.Errorf("%s: %+v, %v", name, out, err)
		}
	}
	f.gh.err = errors.New("github down")
	if out, err := f.runner.Try(t.Context(), cases["cannot read"]); err != nil || !out.Denied {
		t.Errorf("a failed access check must deny: %+v, %v", out, err)
	}
	f.gh.err = nil
	alice := cases["cannot read"]
	alice.Actor = "alice"
	alice.Target.PR = 99
	if out, err := f.runner.Try(t.Context(), alice); err != nil || out.Ran || !strings.Contains(out.Reply, "not found") {
		t.Errorf("missing PR: %+v, %v", out, err)
	}
	if len(f.tr.Calls) != 0 {
		t.Errorf("model called %d times on denied or failed tries", len(f.tr.Calls))
	}
}

type fakeConfigPR struct {
	base, head fstest.MapFS
	changed    []string
}

func (f *fakeConfigPR) Load(context.Context, int) (fs.FS, fs.FS, []string, string, error) {
	return f.base, f.head, f.changed, "head1", nil
}

type fakeReplier struct{ replies []string }

func (f *fakeReplier) Comment(_ context.Context, _ int, body string) error {
	f.replies = append(f.replies, body)
	return nil
}

func TestHandler(t *testing.T) {
	f := setup(t)
	head := baseFS(t)
	head["rules/reliability/no-bare-http-get-c9.yaml"] = &fstest.MapFile{Data: []byte(draftRule)}
	cpr := &fakeConfigPR{base: baseFS(t), head: head, changed: []string{"rules/reliability/no-bare-http-get-c9.yaml"}}
	rep := &fakeReplier{}
	h := &Handler{ConfigRepo: "acme/exemplar-config", Config: cpr, Runner: f.runner, Replier: rep}
	ctx := t.Context()
	cmd := "/exemplar try " + sandboxRepo + "#41"

	for name, c := range map[string]Comment{
		"other repo":  {Repo: "acme/app", PR: 3, Author: "alice", AuthorType: "User", Body: cmd},
		"bot":         {Repo: "acme/exemplar-config", PR: 3, Author: "exemplar[bot]", AuthorType: "Bot", Body: cmd},
		"not command": {Repo: "acme/exemplar-config", PR: 3, Author: "alice", AuthorType: "User", Body: "LGTM"},
	} {
		if handled, err := h.Handle(ctx, c); handled || err != nil {
			t.Errorf("%s: handled = %v, %v", name, handled, err)
		}
	}
	if len(rep.replies) != 0 {
		t.Fatalf("replied to ignored comments: %v", rep.replies)
	}

	if handled, err := h.Handle(ctx, Comment{Repo: "acme/exemplar-config", PR: 3, Author: "alice", AuthorType: "User", Body: "/exemplar try nope"}); !handled || err != nil {
		t.Fatalf("usage: %v, %v", handled, err)
	}
	if handled, err := h.Handle(ctx, Comment{Repo: "Acme/Exemplar-Config", PR: 3, Author: "alice", AuthorType: "User", Body: cmd}); !handled || err != nil {
		t.Fatalf("try: %v, %v", handled, err)
	}
	if len(rep.replies) != 2 || !strings.Contains(rep.replies[0], "usage") || !strings.HasPrefix(rep.replies[1], "@alice **Exemplar try**") {
		t.Fatalf("replies = %q", rep.replies)
	}

	cpr.changed = []string{"README.md"}
	_, _ = h.Handle(ctx, Comment{Repo: "acme/exemplar-config", PR: 3, Author: "alice", AuthorType: "User", Body: cmd})
	if !strings.Contains(rep.replies[2], "changes no rule files") {
		t.Errorf("no-rules reply = %q", rep.replies[2])
	}
}
