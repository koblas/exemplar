package gates

import (
	"slices"
	"sort"
	"strings"

	"github.com/koblas/exemplar/internal/codeowners"
)

// Stage 2 owner reasons.
const (
	ReasonNoApprovableOwners = "no_approvable_owners_configured"
	ReasonUnownedPath        = "unowned_path"
	ReasonOwnerNotApprovable = "owner_not_auto_approvable"
)

// OwnersResult is the Stage 2 CODEOWNERS lookup trace.
type OwnersResult struct {
	Pass    bool     `json:"pass"`
	Reasons []string `json:"reasons,omitempty"`
	// Unowned lists touched paths no CODEOWNERS rule gives an owner.
	Unowned []string `json:"unowned,omitempty"`
	// NotApprovable maps a touched path to its owners that are not in
	// the repo's auto_approve.approvable_owners.
	NotApprovable map[string][]string `json:"not_approvable,omitempty"`
	// Owners maps each owned touched path to its CODEOWNERS owners, so
	// an auto-approved PR shows in their weekly digest (FR-A5).
	Owners map[string][]string `json:"owners,omitempty"`
}

// CheckOwners is Stage 2's CODEOWNERS lookup (spec §6.13): every touched
// path's owners must all be in the repo's auto-approvable set. It fails
// closed: a path with no owner (including when the repo has no CODEOWNERS)
// has nobody to vouch for it, and an empty set approves nothing. co must
// be read from the base commit, so a PR cannot change its own owners.
// Owners compare case-insensitively, as GitHub logins and teams do.
func CheckOwners(touched []string, co codeowners.File, approvable []string) OwnersResult {
	res := OwnersResult{}
	if len(approvable) == 0 {
		res.Reasons = []string{ReasonNoApprovableOwners}
		return res
	}
	ok := map[string]bool{}
	for _, o := range approvable {
		ok[strings.ToLower(o)] = true
	}
	paths := slices.Clone(touched)
	sort.Strings(paths)
	paths = slices.Compact(paths)
	for _, p := range paths {
		owners := co.Owners(p)
		if len(owners) == 0 {
			res.Unowned = append(res.Unowned, p)
			continue
		}
		if res.Owners == nil {
			res.Owners = map[string][]string{}
		}
		res.Owners[p] = owners
		for _, o := range owners {
			if !ok[strings.ToLower(o)] {
				if res.NotApprovable == nil {
					res.NotApprovable = map[string][]string{}
				}
				res.NotApprovable[p] = append(res.NotApprovable[p], o)
			}
		}
	}
	if len(res.Unowned) > 0 {
		res.Reasons = append(res.Reasons, ReasonUnownedPath)
	}
	if len(res.NotApprovable) > 0 {
		res.Reasons = append(res.Reasons, ReasonOwnerNotApprovable)
	}
	res.Pass = len(res.Reasons) == 0
	return res
}
