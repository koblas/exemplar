package gates

import (
	"slices"
	"testing"

	"github.com/koblas/exemplar/internal/codeowners"
)

func TestCheckOwners(t *testing.T) {
	co := codeowners.Parse(`
*            @acme/platform
/docs/       @acme/docs
/svc/pay/    @acme/payments @carol
/vendor/
`)
	approvable := []string{"@acme/Docs", "@acme/platform"}
	tests := []struct {
		name    string
		touched []string
		set     []string
		pass    bool
		reasons []string
	}{
		{"all approvable", []string{"docs/a.md", "README.md", "docs/a.md"}, approvable, true, nil},
		{"case-insensitive", []string{"docs/a.md"}, []string{"@ACME/DOCS"}, true, nil},
		{"one owner not approvable", []string{"docs/a.md", "svc/pay/charge.go"}, approvable, false, []string{ReasonOwnerNotApprovable}},
		{"unowned path", []string{"vendor/x.go"}, approvable, false, []string{ReasonUnownedPath}},
		{"empty set", []string{"docs/a.md"}, nil, false, []string{ReasonNoApprovableOwners}},
	}
	for _, tt := range tests {
		got := CheckOwners(tt.touched, co, tt.set)
		if got.Pass != tt.pass || !slices.Equal(got.Reasons, tt.reasons) {
			t.Errorf("%s: %+v", tt.name, got)
		}
	}
	got := CheckOwners([]string{"svc/pay/charge.go"}, co, approvable)
	if !slices.Equal(got.NotApprovable["svc/pay/charge.go"], []string{"@acme/payments", "@carol"}) {
		t.Errorf("not approvable = %v", got.NotApprovable)
	}
	// No CODEOWNERS at all: nothing is owned, so nothing passes.
	if got := CheckOwners([]string{"a.go"}, codeowners.Parse(""), approvable); got.Pass || !slices.Equal(got.Unowned, []string{"a.go"}) {
		t.Errorf("no CODEOWNERS = %+v", got)
	}
}

func TestCheckBranchRules(t *testing.T) {
	complianceRepos["acme/ledger"] = true
	t.Cleanup(func() { delete(complianceRepos, "acme/ledger") })
	tests := []struct {
		name          string
		repo, mode    string
		rules         *BranchRules
		pass          bool
		reasons       []string
		bot, human    int
		wantHumanNote bool
	}{
		{"one approval", "acme/app", "", &BranchRules{RequiredApprovals: 1}, true, nil, 1, 0, false},
		{"no approval required", "acme/app", ApprovalNative, &BranchRules{}, true, nil, 0, 0, false},
		{"two approvals", "acme/app", ApprovalNative, &BranchRules{RequiredApprovals: 2}, true, nil, 1, 1, true},
		{"unknown rules", "acme/app", ApprovalNative, nil, false, []string{ReasonBranchRulesUnknown}, 0, 0, false},
		{"code owner review", "acme/app", ApprovalNative, &BranchRules{RequiredApprovals: 1, CodeOwnerReview: true}, false, []string{ReasonCodeOwnerReviewRequired}, 1, 0, false},
		{"merge policy required", "acme/app", ApprovalMergePolicy, &BranchRules{CodeOwnerReview: true, RequiredChecks: []string{"ci", MergePolicyCheck}}, true, nil, 0, 0, false},
		{"merge policy missing", "acme/app", ApprovalMergePolicy, &BranchRules{RequiredChecks: []string{"ci"}}, false, []string{ReasonMergePolicyNotRequired}, 0, 0, false},
		{"bad mode", "acme/app", "yolo", &BranchRules{}, false, []string{ReasonUnknownApprovalMode}, 0, 0, false},
		{"compliance two approvals", "Acme/Ledger", ApprovalNative, &BranchRules{RequiredApprovals: 2}, true, nil, 1, 1, true},
		{"compliance one approval", "acme/ledger", ApprovalNative, &BranchRules{RequiredApprovals: 1}, false, []string{ReasonComplianceApprovals}, 1, 0, false},
	}
	for _, tt := range tests {
		got := CheckBranchRules(tt.repo, tt.mode, tt.rules)
		if got.Pass != tt.pass || !slices.Equal(got.Reasons, tt.reasons) || got.BotApprovals != tt.bot ||
			got.HumanApprovals != tt.human || slices.Contains(got.Notes, NoteHumanApprovalStillRequired) != tt.wantHumanNote {
			t.Errorf("%s: %+v", tt.name, got)
		}
	}
}
