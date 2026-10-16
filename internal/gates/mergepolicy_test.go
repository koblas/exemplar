package gates

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/codeowners"
)

func TestHumanMergePolicy(t *testing.T) {
	co := codeowners.Parse("* @acme/eng\n/src/net/ @acme/net @dana\n/legal/ legal@example.com\n")
	member := func(_ context.Context, team, login string) (bool, error) {
		m := map[string][]string{"@acme/eng": {"erin", "nick"}, "@acme/net": {"nick"}}
		if slices.Contains(m[team], login) {
			return true, nil
		}
		return false, nil
	}
	approve := func(user, commit string) PolicyReview {
		return PolicyReview{User: user, UserType: "User", State: "APPROVED", CommitID: commit}
	}
	for _, c := range []struct {
		name    string
		touched []string
		reviews []PolicyReview
		pass    bool
		missing string
	}{
		{"team member approves", []string{"README.md"}, []PolicyReview{approve("erin", "h")}, true, ""},
		{"approval of an older commit", []string{"README.md"}, []PolicyReview{approve("erin", "old")}, false, "an approval of this commit"},
		{"the author's own approval", []string{"README.md"}, []PolicyReview{approve("alice", "h")}, false, "an approval of this commit"},
		{"a bot's approval", []string{"README.md"}, []PolicyReview{{User: "exemplar[bot]", UserType: "Bot", State: "APPROVED", CommitID: "h"}}, false, "an approval of this commit"},
		{
			"approval then changes requested",
			[]string{"README.md"},
			[]PolicyReview{approve("erin", "h"), {User: "erin", UserType: "User", State: "CHANGES_REQUESTED", CommitID: "h"}},
			false, "an approval of this commit",
		},
		{"approval then a comment", []string{"README.md"}, []PolicyReview{approve("erin", "h"), {User: "erin", UserType: "User", State: "COMMENTED", CommitID: "h"}}, true, ""},
		{"dismissed approval", []string{"README.md"}, []PolicyReview{approve("erin", "h"), {User: "erin", UserType: "User", State: "DISMISSED", CommitID: "h"}}, false, "an approval of this commit"},
		{"net path needs net", []string{"src/net/a.go", "README.md"}, []PolicyReview{approve("erin", "h")}, false, "@acme/net @dana"},
		{"user owner", []string{"src/net/a.go"}, []PolicyReview{approve("Dana", "h")}, true, ""},
		{"one approver owns both", []string{"src/net/a.go", "README.md"}, []PolicyReview{approve("nick", "h")}, true, ""},
		{"email owner cannot be matched", []string{"legal/terms.md"}, []PolicyReview{approve("erin", "h")}, false, "legal@example.com"},
	} {
		res, err := HumanMergePolicy(t.Context(), c.touched, co, "alice", "h", c.reviews, member)
		if err != nil || res.Pass != c.pass || strings.Join(res.Missing, ";") != c.missing {
			t.Errorf("%s: %+v, %v", c.name, res, err)
		}
	}
	// No CODEOWNERS: any approver.
	if res, _ := HumanMergePolicy(t.Context(), []string{"a.go"}, codeowners.File{}, "alice", "h", []PolicyReview{approve("bob", "h")}, member); !res.Pass {
		t.Errorf("unowned = %+v", res)
	}
	// A membership lookup failure is an error, not a pass or a fail.
	fail := func(context.Context, string, string) (bool, error) { return false, errors.New("rate limited") }
	if _, err := HumanMergePolicy(t.Context(), []string{"README.md"}, co, "alice", "h", []PolicyReview{approve("erin", "h")}, fail); err == nil {
		t.Error("membership error swallowed")
	}
}
