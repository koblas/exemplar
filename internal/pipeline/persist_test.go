package pipeline

import (
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/feedbacklink"
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
		body := commentBody(f, nil)
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
	body := commentBody(f, nil)
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
	body := commentBody(f, links)
	if !strings.Contains(body, "[Fixed](https://exemplar.example.invalid/f/8812/fixed?s=") || !strings.Contains(body, "or reply `/exemplar <reason>`") {
		t.Errorf("body = %s", body)
	}
	// Not persisted (no id): no links to sign.
	f.ID = 0
	if body := commentBody(f, links); strings.Contains(body, "/f/") {
		t.Errorf("unpersisted body = %s", body)
	}
}
