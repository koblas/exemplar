package evalgen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/scope"
)

const sha1 = "0123456789abcdef0123456789abcdef01234567"

func TestPositives(t *testing.T) {
	got := Positives([]PositiveSource{
		{CommentID: 1, Repo: "acme/pay", PR: 3, Path: "a.go", CommitSHA: sha1, StartLine: 10, EndLine: 12},
		{CommentID: 2, Repo: "acme/pay", PR: 3, Path: "a.go", CommitSHA: sha1, StartLine: 10, EndLine: 12}, // duplicate location
		{CommentID: 3, Repo: "acme/pay", PR: 4, Path: "b.go", CommitSHA: "", StartLine: 5},                 // no commit recorded
		{CommentID: 4, Repo: "acme/pay", PR: 5, Path: "c.go", CommitSHA: sha1, StartLine: 7},               // single line
	})
	if len(got) != 2 {
		t.Fatalf("positives = %+v", got)
	}
	if got[0].ID != "p-1" || got[0].Ref != "acme/pay@"+sha1 || got[0].Expect[0].Lines != [2]int{10, 12} {
		t.Fatalf("positive = %+v", got[0])
	}
	if got[1].Expect[0].Lines != [2]int{7, 7} {
		t.Fatalf("single-line range = %v", got[1].Expect[0].Lines)
	}
}

func TestNegativesAndSpotChecks(t *testing.T) {
	pats, err := scope.CompilePatterns([]string{"**/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	cases, targets := Negatives([]NegativePR{
		{Repo: "acme/pay", PR: 10, HeadSHA: sha1, Files: []string{"README.md", "svc/x.go"}},
		{Repo: "acme/pay", PR: 11, HeadSHA: sha1, Files: []string{"docs/a.md"}}, // nothing in scope
		{Repo: "acme/pay", PR: 12, HeadSHA: "not-a-sha", Files: []string{"y.go"}},
		{Repo: "acme/pay", PR: 13, HeadSHA: sha1, Files: []string{"z.go"}},
	}, pats, 1)
	if len(cases) != 1 || cases[0].ID != "n-acme-pay-10" || len(cases[0].Expect) != 0 {
		t.Fatalf("negatives = %+v", cases)
	}
	if targets["n-acme-pay-10"].Path != "svc/x.go" {
		t.Fatalf("target = %+v", targets)
	}
	// The case names the files the rule runs on: the in-scope ones only.
	if strings.Join(cases[0].Paths, ",") != "svc/x.go" {
		t.Fatalf("paths = %v", cases[0].Paths)
	}

	all := []Case{{Kind: KindPositive}}
	for range 11 {
		all = append(all, Case{Kind: KindInjected})
	}
	MarkSpotChecks(all)
	n := 0
	for _, c := range all {
		if c.SpotCheck {
			n++
			if c.Kind != KindInjected {
				t.Fatal("spot check on a history case")
			}
		}
	}
	if n != 2 { // ceil(11 * 10%)
		t.Fatalf("spot checks = %d, want 2", n)
	}
}

const goFile = `package pay

import "net/http"

func Fetch(c *http.Client, url string) error {
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
`

type fakeModel struct{ edit Edit }

func (f fakeModel) ProposeViolation(context.Context, Rule, string, string) (Edit, error) {
	return f.edit, nil
}

type fakeFiles map[string]string

func (f fakeFiles) FileAt(_ context.Context, _, _, path string) ([]byte, error) {
	s, ok := f[path]
	if !ok {
		return nil, errors.New("no such file")
	}
	return []byte(s), nil
}

func injector(t *testing.T, edit Edit, files fakeFiles) *Injector {
	t.Helper()
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	return &Injector{Model: fakeModel{edit}, Files: files, Scanner: sc}
}

var base = Case{ID: "n-acme-pay-10", Kind: KindNegative, Ref: "acme/pay@" + sha1}

func TestInjectValidEdit(t *testing.T) {
	// Drop the error check: a realistic violation of an error-handling rule.
	edit := Edit{StartLine: 6, EndLine: 9, Replacement: "\tresp, _ := c.Get(url)"}
	inj := injector(t, edit, fakeFiles{"pay/fetch.go": goFile})
	c, err := inj.Inject(t.Context(), Rule{Title: "Check errors"}, base, InjectTarget{Repo: "acme/pay", PR: 10, SHA: sha1, Path: "pay/fetch.go"})
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if c.ID != "i-acme-pay-10" || c.Kind != KindInjected || c.Ref != base.Ref || c.Expect[0].Lines != [2]int{6, 6} {
		t.Fatalf("case = %+v", c)
	}
	// The patch is a real diff that applies to the original file.
	if err := checkApplies(t.Context(), "pay/fetch.go", []byte(goFile), c.Patch); err != nil {
		t.Fatalf("patch does not apply: %v\n%s", err, c.Patch)
	}
	if _, err := scope.Parse(strings.NewReader(c.Patch)); err != nil {
		t.Fatalf("patch does not parse as a diff: %v", err)
	}
}

func TestInjectRejections(t *testing.T) {
	sum := sha256.Sum256([]byte("evalgen token"))
	secretFile := "package pay\n\nconst token = \"ghp_" + hex.EncodeToString(sum[:])[:36] + "\"\n"
	big := strings.Repeat("\tx := 1\n", 13)
	tests := []struct {
		name  string
		path  string
		files fakeFiles
		edit  Edit
		want  string
	}{
		{"does not parse", "a.go", fakeFiles{"a.go": goFile}, Edit{StartLine: 5, EndLine: 5, Replacement: "func Fetch(c *http.Client, url string error {"}, "does not parse"},
		{"no parser for language", "a.rb", fakeFiles{"a.rb": "puts 1\n"}, Edit{StartLine: 1, EndLine: 1, Replacement: "puts 2"}, "no parser"},
		{"file with a secret", "s.go", fakeFiles{"s.go": secretFile}, Edit{StartLine: 3, EndLine: 3, Replacement: "const token = \"\""}, "secret"},
		{"out of range", "a.go", fakeFiles{"a.go": goFile}, Edit{StartLine: 40, EndLine: 41, Replacement: "x"}, "outside"},
		{"no-op", "a.go", fakeFiles{"a.go": goFile}, Edit{StartLine: 3, EndLine: 3, Replacement: `import "net/http"`}, "changes nothing"},
		{"too large", "a.go", fakeFiles{"a.go": goFile}, Edit{StartLine: 6, EndLine: 9, Replacement: big}, "more than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := injector(t, tt.edit, tt.files).Inject(t.Context(), Rule{}, base, InjectTarget{Repo: "acme/pay", SHA: sha1, Path: tt.path})
			if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Inject = %v, want rejection containing %q", err, tt.want)
			}
		})
	}
}

func TestCountChanged(t *testing.T) {
	if n := countChanged([]string{"a", "b", "c"}, []string{"a", "x", "c"}); n != 2 {
		t.Fatalf("countChanged = %d", n)
	}
	if n := countChanged([]string{"a"}, nil); n != 1 {
		t.Fatalf("deletion = %d", n)
	}
}

func TestPositivesFromRevertSeeds(t *testing.T) {
	got := Positives([]PositiveSource{{CommentID: -77, Repo: "acme/pay", PR: 7, Path: "a.go", CommitSHA: sha1, StartLine: 20, EndLine: 22}})
	if len(got) != 1 || got[0].ID != "p-r77" || got[0].Name != "reverted change in acme/pay#7" {
		t.Fatalf("Positives = %+v", got)
	}
}
