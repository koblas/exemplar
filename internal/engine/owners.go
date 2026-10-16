package engine

import (
	"io/fs"

	"github.com/koblas/exemplar/internal/codeowners"
	"github.com/koblas/exemplar/internal/gates"
)

// baseCodeOwners returns the CODEOWNERS GitHub would use at the base
// commit: the first of codeowners.Locations that exists.
func baseCodeOwners(base fs.FS) codeowners.File {
	for _, loc := range codeowners.Locations {
		if data, err := fs.ReadFile(base, loc); err == nil {
			return codeowners.Parse(string(data))
		}
	}
	return codeowners.File{}
}

// Touched lists every path the PR touches: both sides of a rename, and
// excluded and binary files too, since an owner check that skipped them
// would let them through unowned.
func (p *Prepared) Touched() []string {
	var out []string
	for _, f := range p.Diff.Files() {
		out = append(out, f.Path)
		if f.OldPath != "" && f.OldPath != f.Path {
			out = append(out, f.OldPath)
		}
	}
	return out
}

// CheckOwners runs the auto-approve CODEOWNERS lookup against the
// base commit's CODEOWNERS and the repo's auto_approve.approvable_owners.
func (p *Prepared) CheckOwners() gates.OwnersResult {
	var approvable []string
	if aa := p.Effective.RepoConfig.AutoApprove; aa != nil {
		approvable = aa.ApprovableOwners
	}
	return gates.CheckOwners(p.Touched(), p.CodeOwners, approvable)
}
