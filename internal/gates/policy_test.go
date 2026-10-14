package gates

import (
	"slices"
	"testing"
)

func TestDenylisted(t *testing.T) {
	denied := []string{
		".github/workflows/ci.yml", ".github/copilot-instructions.md", ".gitlab-ci.yml", ".circleci/config.yml",
		"svc/CODEOWNERS", "docs/CODEOWNERS", "Jenkinsfile", "lib/BUILD.bazel", "tools/defs.bzl", "WORKSPACE",
		"Makefile", "sub/makefile", "CMakeLists.txt", "go.mod", "svc/go.sum", "web/package.json", "web/package-lock.json",
		"yarn.lock", "requirements-dev.txt", "pyproject.toml", "Cargo.toml", "pom.xml", "build.gradle.kts",
		"AGENTS.md", "svc/CLAUDE.md", ".cursor/rules/style.mdc", ".exemplar/rules/x.yaml", ".gitattributes",
		"config/prod/flags.yaml", // extra
	}
	for _, p := range denied {
		if !Denylisted("acme/app", p, []string{"config/prod/**"}) {
			t.Errorf("%s not denylisted", p)
		}
	}
	for _, p := range []string{"docs/guide.md", "README.md", "svc/handler.go", "config/dev/flags.yaml", "docs/github.md"} {
		if Denylisted("acme/app", p, []string{"config/prod/**"}) {
			t.Errorf("%s denylisted", p)
		}
	}
	if !Denylisted("acme/exemplar-config", "docs/guide.md", nil) {
		t.Error("a path in exemplar-config is not denylisted")
	}
}

func policyIn() PolicyInput {
	return PolicyInput{
		Repo: "acme/app", Touched: []string{"docs/a.md", "docs/b.md"}, Files: 2, ChangedLines: 30,
		Author: "alice", AuthorType: "User", AuthorTeams: map[string]bool{"@acme/docs": true},
		CommitMessages: []string{"docs: fix typos"}, HeadRepo: "acme/app", Title: "docs: fix typos",
		Policy: AutoApprovePolicy{
			Paths: []string{"docs/**", "config/flags/**"}, AllowedAuthors: []string{"@acme/docs", "bob"},
			BotAuthors:   []BotIdentity{{Identity: "renovate[bot]", Paths: []string{"config/flags/**"}}, {Identity: "noreply@anthropic.com", Paths: []string{"docs/**"}}},
			TitlePattern: "^docs: ",
		},
	}
}

func TestCheckPolicy(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(*PolicyInput)
		reasons []string
	}{
		{"human on a team", func(*PolicyInput) {}, nil},
		{"human by login", func(in *PolicyInput) { in.Author, in.AuthorTeams = "Bob", map[string]bool{"@acme/docs": false} }, nil},
		{"human not allowed", func(in *PolicyInput) { in.Author, in.AuthorTeams = "mallory", map[string]bool{"@acme/docs": false} }, []string{ReasonAuthorNotAllowed}},
		{"team membership unknown", func(in *PolicyInput) { in.Author, in.AuthorTeams = "carol", nil }, []string{ReasonAuthorTeamUnknown}},
		{"author unknown", func(in *PolicyInput) { in.AuthorType = "" }, []string{ReasonAuthorUnknown}},
		{"commits unread", func(in *PolicyInput) { in.CommitMessages = nil }, []string{ReasonCommitsUnknown}},
		{"path outside paths", func(in *PolicyInput) { in.Touched = []string{"docs/a.md", "svc/x.go"} }, []string{ReasonPathNotAllowed}},
		{"denylisted path inside paths", func(in *PolicyInput) { in.Touched = []string{"docs/CODEOWNERS"} }, []string{ReasonPathDenylisted}},
		{"no paths", func(in *PolicyInput) { in.Touched = nil }, []string{ReasonPathNotAllowed}},
		{"fork", func(in *PolicyInput) { in.HeadRepo = "mallory/app" }, []string{ReasonFork}},
		{"head unknown", func(in *PolicyInput) { in.HeadRepo = "" }, []string{ReasonForkUnknown}},
		{"too many files", func(in *PolicyInput) { in.Files = 11 }, []string{ReasonTooManyFiles}},
		{"too many lines", func(in *PolicyInput) { in.ChangedLines = 1001 }, []string{ReasonTooManyLines}},
		{"repo lowers the file limit", func(in *PolicyInput) { in.Policy.MaxFiles = 1 }, []string{ReasonTooManyFiles}},
		{"repo cannot raise the file limit", func(in *PolicyInput) { in.Files, in.Policy.MaxFiles = 20, 50 }, []string{ReasonTooManyFiles}},
		{"bot in its scope", func(in *PolicyInput) {
			in.Author, in.AuthorType, in.Touched = "renovate[bot]", "Bot", []string{"config/flags/a.yaml"}
		}, nil},
		{"bot outside its scope", func(in *PolicyInput) { in.Author, in.AuthorType = "renovate[bot]", "Bot" }, []string{ReasonBotPathOutOfScope}},
		{"bot by login suffix only", func(in *PolicyInput) { in.Author = "renovate[bot]" }, []string{ReasonBotPathOutOfScope}},
		{"unlisted bot", func(in *PolicyInput) { in.Author, in.AuthorType = "dependabot[bot]", "Bot" }, []string{ReasonBotNotAllowlisted}},
		{"allowlisted agent co-author", func(in *PolicyInput) {
			in.CommitMessages = []string{"docs: x\n\nCo-authored-by: Claude Opus 5.5 <noreply@anthropic.com>"}
		}, nil},
		{"unlisted agent co-author", func(in *PolicyInput) {
			in.CommitMessages = []string{"docs: x", "fix\n\nCo-Authored-By: Copilot <198982749+Copilot@users.noreply.github.com>"}
		}, []string{ReasonBotNotAllowlisted}},
		{"agent co-author outside its scope", func(in *PolicyInput) {
			in.Touched = []string{"config/flags/a.yaml"}
			in.CommitMessages = []string{"Co-authored-by: Claude <noreply@anthropic.com>"}
		}, []string{ReasonBotPathOutOfScope}},
		{"agent co-author does not excuse the human", func(in *PolicyInput) {
			in.Author, in.AuthorTeams = "mallory", map[string]bool{"@acme/docs": false}
			in.CommitMessages = []string{"Co-authored-by: Claude <noreply@anthropic.com>"}
		}, []string{ReasonAuthorNotAllowed}},
		{"human co-author is not an agent", func(in *PolicyInput) {
			in.CommitMessages = []string{"docs\n\nCo-authored-by: Dana <dana@acme.example>"}
		}, nil},
	}
	for _, tt := range tests {
		in := policyIn()
		tt.edit(&in)
		got := CheckPolicy(in)
		if !slices.Equal(got.Reasons, tt.reasons) && len(got.Reasons)+len(tt.reasons) > 0 {
			t.Errorf("%s: reasons = %v, want %v (%+v)", tt.name, got.Reasons, tt.reasons, got)
		}
		pass := got.PathsOK && got.AuthorOK && got.NotFork && got.SizeOK
		if pass != (len(tt.reasons) == 0) {
			t.Errorf("%s: flags %+v disagree with reasons %v", tt.name, got, got.Reasons)
		}
	}
}

func TestCheckPolicyTrace(t *testing.T) {
	in := policyIn()
	in.Touched = []string{"docs/a.md", "svc/x.go", "go.mod"}
	in.Title = "feat: things"
	got := CheckPolicy(in)
	if !slices.Equal(got.NotAllowed, []string{"go.mod", "svc/x.go"}) || !slices.Equal(got.Denylisted, []string{"go.mod"}) {
		t.Errorf("paths = %+v", got)
	}
	if got.TitleMatches == nil || *got.TitleMatches || got.MaxFiles != 10 || got.MaxChangedLines != 1000 || got.AuthorKind != AuthorHuman {
		t.Errorf("trace = %+v", got)
	}
	// title_pattern is informational: a mismatch is not a reason.
	in = policyIn()
	in.Title = "anything"
	if got := CheckPolicy(in); len(got.Reasons) != 0 {
		t.Errorf("title gated: %v", got.Reasons)
	}
}

func TestAgentCoAuthors(t *testing.T) {
	msg := "Fix\n\nCo-authored-by: Claude Opus 5.5 <noreply@anthropic.com>\nco-authored-by: Dana <dana@acme.example>\n  Co-Authored-By: cursoragent\nSigned-off-by: Dana <dana@acme.example>"
	if got := AgentCoAuthors(msg); !slices.Equal(got, []string{"noreply@anthropic.com", "cursoragent"}) {
		t.Errorf("agents = %v", got)
	}
	if got := AgentCoAuthors("Mentions Co-authored-by: Claude inline"); got != nil {
		t.Errorf("inline mention = %v", got)
	}
}
