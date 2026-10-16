// Package playground runs draft rules from exemplar-config PRs on real PRs
// (/exemplar try org/repo#1234, or the exemplar try CLI). A run
// checks that the person asking can read the target repo and that the repo
// is eligible, runs only the draft rules, posts nothing on the target PR,
// and answers with file paths, line numbers and finding messages: never
// code, because exemplar-config is readable by more people than most
// target repos.
package playground

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"testing/fstest"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
)

// Draftable prefixes: a config PR's changes are taken from these
// directories only. Everything else (global.yaml, platform/eligibility.yaml,
// repos/) comes from the base branch, so a PR cannot widen its own reach
// by editing the denylist or a repo's settings.
var draftable = []string{"rules/", "evals/"}

// MaxDraftRules bounds how many changed rules one run tries.
const MaxDraftRules = 3

// ErrNoDraftRules means the PR changes no rule files.
var ErrNoDraftRules = errors.New("playground: the PR changes no rule files")

// DraftSnapshot loads exemplar-config as of base with the PR's changes to
// rules/ and evals/ applied from head, and returns the ids of the changed
// rules. changed lists the PR's changed paths; a path missing from head
// was deleted.
func DraftSnapshot(base, head fs.FS, changed []string, version string) (*config.Snapshot, []string, error) {
	overlay := fstest.MapFS{}
	err := fs.WalkDir(base, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(base, p)
		if err != nil {
			return err
		}
		overlay[p] = &fstest.MapFile{Data: data}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("playground: read base config: %w", err)
	}
	var rulePaths []string
	for _, p := range changed {
		if !isDraftable(p) {
			continue
		}
		data, err := fs.ReadFile(head, p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			delete(overlay, p)
			continue
		case err != nil:
			return nil, nil, fmt.Errorf("playground: read %s at head: %w", p, err)
		}
		overlay[p] = &fstest.MapFile{Data: data}
		if strings.HasPrefix(p, "rules/") && path.Ext(p) == ".yaml" {
			rulePaths = append(rulePaths, p)
		}
	}
	snap, err := config.LoadSnapshot(overlay, version)
	if err != nil {
		return nil, nil, err
	}
	ids, err := ruleIDs(snap, rulePaths)
	return snap, ids, err
}

// RuleFileSnapshot loads exemplar-config from dir with one extra rule file
// (exemplar try <rule.yaml>), placed where exemplar-config keeps rules of
// its category. It replaces a rule with the same id.
func RuleFileSnapshot(dir fs.FS, ruleYAML []byte, version string) (*config.Snapshot, string, error) {
	var r schema.Rule
	if err := schema.Decode(schema.DocRule, ruleYAML, &r); err != nil {
		return nil, "", fmt.Errorf("playground: rule file: %w", err)
	}
	overlay := fstest.MapFS{}
	err := fs.WalkDir(dir, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(dir, p)
		if err != nil {
			return err
		}
		overlay[p] = &fstest.MapFile{Data: data}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	snap, err := config.LoadSnapshot(overlay, version)
	if err != nil {
		return nil, "", err
	}
	for _, existing := range snap.Rules {
		if existing.ID == r.ID {
			delete(overlay, existing.Path)
		}
	}
	p := fmt.Sprintf("rules/%s/%s.yaml", strings.ToLower(r.Category), r.ID)
	overlay[p] = &fstest.MapFile{Data: ruleYAML}
	if snap, err = config.LoadSnapshot(overlay, version); err != nil {
		return nil, "", err
	}
	return snap, r.ID, nil
}

func isDraftable(p string) bool {
	for _, d := range draftable {
		if strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}

func ruleIDs(snap *config.Snapshot, paths []string) ([]string, error) {
	var ids []string
	for _, p := range paths {
		for _, r := range snap.Rules {
			if r.Path == p {
				ids = append(ids, r.ID)
			}
		}
	}
	if len(ids) == 0 {
		return nil, ErrNoDraftRules
	}
	if len(ids) > MaxDraftRules {
		return nil, fmt.Errorf("playground: the PR changes %d rules; try runs at most %d", len(ids), MaxDraftRules)
	}
	return ids, nil
}
