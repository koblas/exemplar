package exports

import (
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
)

func rule(id, severity, summary string, patterns ...string) config.Rule {
	return config.Rule{Rule: schema.Rule{ID: id, Severity: severity, Status: schema.StatusActive, FilePatterns: patterns,
		Export: &schema.Export{Agents: true, Summary: summary}}}
}

func TestArea(t *testing.T) {
	for _, c := range []struct {
		patterns []string
		want     string
	}{
		{[]string{"**/*.go", "!**/*_test.go"}, ""},
		{[]string{"src/net/**"}, "src/net"},
		{[]string{"src/net/**", "src/net/*.cc"}, "src/net"},
		{[]string{"src/net/**", "src/sim/**"}, "src"},
		{[]string{"src/net/**", "tools/**"}, ""},
		{[]string{"*.md"}, ""},
	} {
		if got := area(c.patterns); got != c.want {
			t.Errorf("area(%v) = %q, want %q", c.patterns, got, c.want)
		}
	}
}

func TestRender(t *testing.T) {
	rules := []config.Rule{
		rule("propagate-context", "important", "Pass ctx to outbound calls.", "**/*.go"),
		rule("no-raw-sql", "blocking", "Use placeholders, never string-built SQL.", "**/*.go"),
		rule("fetch-in-loop", "suggestion", "Batch fetches in the tick loop.", "src/net/**"),
	}
	files := Render(rules, []string{AgentsMD, ClaudeMD, Copilot, Cursor})
	byPath := map[string]File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	want := []string{"AGENTS.md", "src/net/AGENTS.md", "CLAUDE.md", "src/net/CLAUDE.md", ".github/copilot-instructions.md",
		".github/instructions/src-net.instructions.md", ".cursor/rules/exemplar.mdc", ".cursor/rules/exemplar-src-net.mdc"}
	if len(files) != len(want) {
		t.Fatalf("files = %d", len(files))
	}
	for _, p := range want {
		if _, ok := byPath[p]; !ok {
			t.Errorf("no %s", p)
		}
	}
	root := byPath["AGENTS.md"]
	// Severity first: blocking before important.
	if strings.Join(root.Rules, ",") != "no-raw-sql,propagate-context" || !strings.HasPrefix(root.Block, Begin) || !strings.HasSuffix(root.Block, End+"\n") ||
		!strings.Contains(root.Block, "- **blocking**: Use placeholders, never string-built SQL. (`no-raw-sql`)") || root.Frontmatter != "" {
		t.Errorf("root = %+v", root)
	}
	if f := byPath[".github/instructions/src-net.instructions.md"]; f.Frontmatter != "---\napplyTo: \"src/net/**\"\n---\n\n" || strings.Join(f.Rules, ",") != "fetch-in-loop" {
		t.Errorf("copilot area = %+v", f)
	}
	if f := byPath[".cursor/rules/exemplar-src-net.mdc"]; !strings.Contains(f.Frontmatter, "globs: src/net/**") {
		t.Errorf("cursor area = %+v", f)
	}
}

func TestBudget(t *testing.T) {
	var rules []config.Rule
	for i := range 80 {
		rules = append(rules, rule(strings.Repeat("r", 10)+string(rune('a'+i%26))+string(rune('a'+i/26)), "important", strings.Repeat("Do the thing properly. ", 3), "**/*.go"))
	}
	f := Render(rules, []string{Copilot})[0]
	if len(f.Block) > CopilotBudget || f.Dropped == 0 || len(f.Rules)+f.Dropped != 80 || !strings.Contains(f.Block, "more rules are not shown here") {
		t.Errorf("block %d chars, %d kept, %d dropped", len(f.Block), len(f.Rules), f.Dropped)
	}
	if g := Render(rules, []string{AgentsMD})[0]; len(g.Block) > DefaultBudget || len(g.Rules) <= len(f.Rules) {
		t.Errorf("agents %d chars, %d kept", len(g.Block), len(g.Rules))
	}
}

func TestMerge(t *testing.T) {
	f := File{Block: Begin + "\nnew\n" + End + "\n", Frontmatter: "---\nx: y\n---\n\n"}
	tests := []struct {
		name, in string
		exists   bool
		want     string
	}{
		{"new file", "", false, f.Frontmatter + f.Block},
		{"empty file", "\n", true, f.Block},
		{"team text, no block", "# Team\n\nOur notes.\n", true, "# Team\n\nOur notes.\n\n" + f.Block},
		{"no final newline", "# Team", true, "# Team\n\n" + f.Block},
		{"replace the block, keep the rest", "# Team\n\n" + Begin + "\nold\n" + End + "\n\nMore of ours.\n", true, "# Team\n\n" + f.Block + "\nMore of ours.\n"},
		{"an older marker version", "<!-- BEGIN exemplar v0 -->\nold\n" + End + "\n", true, f.Block},
	}
	for _, tt := range tests {
		if got := Merge(tt.in, tt.exists, f); got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
	}
	if Current("# Team\n"+f.Block+"tail\n") != f.Block || Current("# none\n") != "" {
		t.Error("current")
	}
}
