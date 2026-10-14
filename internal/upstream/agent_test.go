package upstream

import (
	"slices"
	"testing"
)

func TestDetect(t *testing.T) {
	for _, c := range []struct {
		name    string
		author  string
		labels  []string
		commits []string
		want    string
	}{
		{"claude trailer", "alice", nil, []string{"Fix it\n\nCo-Authored-By: Claude <noreply@anthropic.com>"}, "claude_code"},
		{"anthropic email only", "alice", nil, []string{"x\n\nco-authored-by: Bot <noreply@anthropic.com>"}, "claude_code"},
		{"cursor trailer", "alice", nil, []string{"a", "b\n\nCo-authored-by: Cursor Agent <cursoragent@cursor.com>"}, "cursor"},
		{"copilot author", "Copilot", nil, nil, "copilot"},
		{"copilot bot", "copilot-swe-agent[bot]", nil, nil, "copilot"},
		{"devin bot", "devin-ai-integration[bot]", nil, nil, "devin"},
		{"codex label", "alice", []string{"bug", "codex"}, nil, "codex"},
		{"agent label", "alice", []string{"agent:cursor"}, nil, "cursor"},
		{"unknown agent label", "alice", []string{"agent: aider"}, nil, "aider"},
		{"human co-author", "alice", nil, []string{"x\n\nCo-authored-by: Bob <bob@example.com>"}, ""},
		{"agent named in prose only", "alice", nil, []string{"Port the Claude client"}, ""},
		{"long label mentioning an agent", "alice", []string{"needs-claude-review-later"}, nil, ""},
		{"human author named like a bot", "copilotfan", nil, nil, ""},
		{"nothing", "alice", nil, nil, ""},
	} {
		if got := Detect(c.author, c.labels, c.commits); got != c.want {
			t.Errorf("%s: Detect = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestReads(t *testing.T) {
	if r := Reads("claude_code"); !slices.Equal(r, []string{"claude_md"}) {
		t.Errorf("claude_code reads %v", r)
	}
	if r := Reads("aider"); !slices.Equal(r, []string{"agents_md"}) {
		t.Errorf("unknown agent reads %v", r)
	}
	if Reads("") != nil {
		t.Error("no agent reads something")
	}
}

func TestJudge(t *testing.T) {
	agents := func(withPRs, withV, withoutPRs, withoutV int) Comparison {
		return Comparison{Name: "agents", WithPRs: withPRs, WithViolations: withV, WithoutPRs: withoutPRs, WithoutViols: withoutV}
	}
	for _, c := range []struct {
		name string
		cs   []Comparison
		want bool
		n    int
	}{
		{"working", []Comparison{agents(40, 4, 40, 20)}, false, 1},
		{"no drop", []Comparison{agents(40, 18, 40, 20)}, true, 1},
		{"too few PRs", []Comparison{agents(10, 9, 40, 10)}, false, 0},
		{"nothing to drop", []Comparison{agents(40, 0, 40, 0)}, false, 0},
		{"one comparison drops", []Comparison{agents(40, 18, 40, 20), {Name: "before/after", WithPRs: 30, WithViolations: 3, WithoutPRs: 30, WithoutViols: 15}}, false, 2},
		{"one measurable, no drop", []Comparison{agents(40, 20, 40, 20), {Name: "before/after", WithPRs: 2, WithViolations: 0, WithoutPRs: 30, WithoutViols: 15}}, true, 1},
	} {
		v := Judge(c.cs...)
		if v.Rewrite != c.want || len(v.Evidence) != c.n {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
}
