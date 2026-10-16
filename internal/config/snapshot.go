// Package config loads exemplar-config and target-repo rules into the
// effective configuration for one review.
//
// Rules and repo config are read from the default branch of
// exemplar-config and from the base commit of the target repo, never from
// a PR head. Callers pass an fs.FS of the right tree; GitFS builds
// one from a commit so the head tree is never consulted.
package config

import (
	"crypto/sha1" //nolint:gosec // git blob ids are SHA-1 by definition
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/koblas/exemplar/internal/buglane"
	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
)

// Rule is a validated rule plus where it came from.
type Rule struct {
	schema.Rule
	// Path is the file the rule was read from, relative to its tree.
	Path string
	// Scope is "global" for exemplar-config rules or "<org>/<repo>" for
	// rules from a target repo's .exemplar/rules.
	Scope string
	// VersionSHA is the git blob id of the rule file, so a finding records
	// exactly which rule text produced it.
	VersionSHA string
	Patterns   scope.Patterns
}

// Snapshot is one commit of exemplar-config, fully validated.
type Snapshot struct {
	// Version is the exemplar-config commit (reviews.config_version).
	Version     string
	Global      schema.GlobalConfig
	Repos       map[string]schema.RepoConfig
	Rules       []Rule
	Evals       map[string]schema.EvalCases
	Eligibility schema.Eligibility
	Retention   schema.Retention
	Adversarial []AdversarialCase
	// VerifierLabels is the verifier calibration seed set (evals/_verifier).
	VerifierLabels []VerifierLabel
	// CategorizerLabels is the auto-approve risk categorizer calibration seed
	// set (evals/_categorizer).
	CategorizerLabels []CategorizerLabel
}

// CategorizerLabel is a labeled PR and the file it came from.
type CategorizerLabel struct {
	schema.CategorizerLabel
	Path string
}

// VerifierLabel is a labeled finding and the file it came from.
type VerifierLabel struct {
	schema.VerifierLabel
	Path string
}

// AdversarialCase is a decoded case from evals/_adversarial.
type AdversarialCase struct {
	schema.AdversarialCase
	Path string
}

// Problem is one validation failure in a config tree.
type Problem struct {
	Path string
	Err  error
}

func (p Problem) Error() string { return p.Path + ": " + p.Err.Error() }

// Problems collects every validation failure so one run reports them all.
type Problems []Problem

func (ps Problems) Error() string {
	msgs := make([]string, len(ps))
	for i, p := range ps {
		msgs[i] = p.Error()
	}
	return strings.Join(msgs, "\n")
}

func (ps *Problems) add(p string, err error) { *ps = append(*ps, Problem{Path: p, Err: err}) }

// Required files in an exemplar-config tree.
const (
	GlobalPath      = "global.yaml"
	EligibilityPath = "platform/eligibility.yaml"
	RetentionPath   = "platform/retention.yaml"
	AdversarialDir  = "evals/_adversarial"
	VerifierDir     = "evals/_verifier"
	CategorizerDir  = "evals/_categorizer"
)

// LoadSnapshot reads and validates an exemplar-config tree. On any problem it
// returns a Problems error listing all of them.
func LoadSnapshot(fsys fs.FS, version string) (*Snapshot, error) {
	s := &Snapshot{
		Version: version,
		Repos:   map[string]schema.RepoConfig{},
		Evals:   map[string]schema.EvalCases{},
	}
	var probs Problems

	decodeFile(fsys, GlobalPath, schema.DocGlobal, &s.Global, &probs)
	decodeFile(fsys, EligibilityPath, schema.DocEligibility, &s.Eligibility, &probs)
	decodeFile(fsys, RetentionPath, schema.DocRetention, &s.Retention, &probs)

	walkYAML(fsys, "repos", &probs, func(p string, data []byte) {
		rel := strings.TrimSuffix(strings.TrimPrefix(p, "repos/"), ".yaml")
		if strings.Count(rel, "/") != 1 {
			probs.add(p, errors.New("repo config must be at repos/<org>/<repo>.yaml"))
			return
		}
		var rc schema.RepoConfig
		if err := schema.Decode(schema.DocRepo, data, &rc); err != nil {
			probs.add(p, err)
			return
		}
		s.Repos[rel] = rc
	})

	walkYAML(fsys, "rules", &probs, func(p string, data []byte) {
		r, err := parseRule(p, data, "global")
		if err != nil {
			probs.add(p, err)
			return
		}
		if want := path.Join("rules", strings.ToLower(r.Category), r.ID+".yaml"); p != want {
			probs.add(p, fmt.Errorf("rule %s with category %s must be at %s", r.ID, r.Category, want))
		}
		s.Rules = append(s.Rules, r)
	})

	walkYAML(fsys, "evals", &probs, func(p string, data []byte) {
		if strings.HasPrefix(p, AdversarialDir+"/") {
			var c schema.AdversarialCase
			if err := schema.Decode(schema.DocAdversarial, data, &c); err != nil {
				probs.add(p, err)
				return
			}
			s.Adversarial = append(s.Adversarial, AdversarialCase{AdversarialCase: c, Path: p})
			return
		}
		if strings.HasPrefix(p, CategorizerDir+"/") {
			var l schema.CategorizerLabel
			if err := schema.Decode(schema.DocCategorizerLabel, data, &l); err != nil {
				probs.add(p, err)
				return
			}
			s.CategorizerLabels = append(s.CategorizerLabels, CategorizerLabel{CategorizerLabel: l, Path: p})
			return
		}
		if strings.HasPrefix(p, VerifierDir+"/") {
			var l schema.VerifierLabel
			if err := schema.Decode(schema.DocVerifierLabel, data, &l); err != nil {
				probs.add(p, err)
				return
			}
			s.VerifierLabels = append(s.VerifierLabels, VerifierLabel{VerifierLabel: l, Path: p})
			return
		}
		if path.Base(p) != "cases.yaml" {
			return // patches and other case assets
		}
		var ec schema.EvalCases
		if err := schema.Decode(schema.DocEvalCases, data, &ec); err != nil {
			probs.add(p, err)
			return
		}
		if want := path.Join("evals", ec.Rule, "cases.yaml"); p != want {
			probs.add(p, fmt.Errorf("eval cases for %s must be at %s", ec.Rule, want))
		}
		s.Evals[ec.Rule] = ec
	})

	s.crossCheck(&probs)
	if len(probs) > 0 {
		return nil, probs
	}
	slices.SortFunc(s.Rules, func(a, b Rule) int { return strings.Compare(a.ID, b.ID) })
	return s, nil
}

func (s *Snapshot) crossCheck(probs *Problems) {
	if bl := s.Global.BugLane; bl != nil {
		for id := range bl.Classes {
			if _, ok := buglane.ByID(id); !ok {
				probs.add(GlobalPath, fmt.Errorf("bug_lane.classes: %s is not an allowlisted bug class", id))
			}
		}
	}
	if ms := s.Global.MergeSafety; ms != nil {
		if _, err := gates.Weights(ms.Weights); err != nil {
			probs.add(GlobalPath, err)
		}
	}
	for repo, rc := range s.Repos {
		for _, g := range rc.SensitivePaths {
			if !doublestar.ValidatePattern(g) {
				probs.add("repos/"+repo+".yaml", fmt.Errorf("sensitive_paths pattern %q is invalid", g))
			}
		}
		// Approval-check bars can be raised per repo, never lowered; the
		// deterministic gates' size limits can be lowered, never raised.
		if aa := rc.AutoApprove; aa != nil {
			rp := "repos/" + repo + ".yaml"
			globs := map[string][]string{"auto_approve.paths": aa.Paths, "auto_approve.extra_denylist": aa.ExtraDenylist}
			for _, b := range aa.BotAuthors {
				globs["auto_approve.bot_authors["+b.Identity+"].paths"] = b.Paths
			}
			for field, gs := range globs {
				for _, g := range gs {
					if !doublestar.ValidatePattern(g) {
						probs.add(rp, fmt.Errorf("%s pattern %q is invalid", field, g))
					}
				}
			}
			if aa.TitlePattern != "" {
				if _, err := regexp.Compile(aa.TitlePattern); err != nil {
					probs.add(rp, fmt.Errorf("auto_approve.title_pattern: %w", err))
				}
			}
			if aa.MaxFiles > gates.MaxFiles {
				probs.add(rp, fmt.Errorf("auto_approve.max_files %d is above the %d limit", aa.MaxFiles, gates.MaxFiles))
			}
			if aa.MaxChangedLines > gates.MaxChangedLines {
				probs.add(rp, fmt.Errorf("auto_approve.max_changed_lines %d is above the %d limit", aa.MaxChangedLines, gates.MaxChangedLines))
			}
			if v := aa.MinMergeSafetyScore; v != 0 && v < gates.DefaultMinMergeSafety {
				probs.add("repos/"+repo+".yaml", fmt.Errorf("auto_approve.min_merge_safety_score %v is below the %v floor", v, gates.DefaultMinMergeSafety))
			}
			if v := aa.MinReviewConfidence; v != 0 && v < gates.DefaultMinReviewConfidence {
				probs.add("repos/"+repo+".yaml", fmt.Errorf("auto_approve.min_review_confidence %v is below the %v floor", v, gates.DefaultMinReviewConfidence))
			}
		}
	}
	if c := s.Global.EvalThresholds.Categorizer; c != nil && c.MinReviewConfidence != 0 && c.MinReviewConfidence < gates.DefaultMinReviewConfidence {
		probs.add(GlobalPath, fmt.Errorf("eval_thresholds.categorizer.min_review_confidence %v is below the %v floor", c.MinReviewConfidence, gates.DefaultMinReviewConfidence))
	}
	for _, r := range s.Rules {
		if r.Export == nil {
			continue
		}
		if r.Export.Agents && strings.TrimSpace(r.Export.Summary) == "" {
			probs.add(r.Path, errors.New("export.agents is true but export.summary is empty"))
		} else if r.Export.Summary != "" {
			if err := LintExportSummary(r.Export.Summary); err != nil {
				probs.add(r.Path, err)
			}
		}
	}
	ids := map[string]string{}
	for _, r := range s.Rules {
		if prev, dup := ids[r.ID]; dup {
			probs.add(r.Path, fmt.Errorf("rule id %s already defined in %s", r.ID, prev))
		}
		ids[r.ID] = r.Path
	}
	for rule := range s.Evals {
		// Bug classes are built-in rules with eval sets of their own
		// (mutation evals).
		if bl := s.Global.BugLane; bl != nil {
			if _, ok := bl.Classes[rule]; ok {
				continue
			}
		}
		if _, ok := ids[rule]; !ok {
			probs.add(path.Join("evals", rule, "cases.yaml"), fmt.Errorf("eval cases for unknown rule %s", rule))
		}
	}
	for _, r := range s.Rules {
		if r.EvalCases == "" {
			continue
		}
		if _, ok := s.Evals[r.ID]; !ok {
			probs.add(r.Path, fmt.Errorf("eval_cases %s has no cases.yaml for this rule", r.EvalCases))
		}
	}
	for repo, rc := range s.Repos {
		for _, id := range rc.DisabledGlobalRules {
			if _, ok := ids[id]; !ok {
				probs.add("repos/"+repo+".yaml", fmt.Errorf("disabled_global_rules names unknown rule %s", id))
			}
		}
	}
	denied := map[string]bool{}
	for _, d := range s.Eligibility.Denylist {
		if denied[d.Repo] {
			probs.add(EligibilityPath, fmt.Errorf("repo %s listed twice", d.Repo))
		}
		denied[d.Repo] = true
	}
	labelIDs := map[string]bool{}
	for _, l := range s.VerifierLabels {
		if labelIDs[l.ID] {
			probs.add(l.Path, fmt.Errorf("verifier label id %s already used", l.ID))
		}
		labelIDs[l.ID] = true
		if _, ok := ids[l.Candidate.Rule]; !ok {
			probs.add(l.Path, fmt.Errorf("label names unknown rule %s", l.Candidate.Rule))
		}
		if l.Candidate.EndLine < l.Candidate.StartLine {
			probs.add(l.Path, errors.New("candidate end_line is before start_line"))
		}
	}
	catIDs := map[string]bool{}
	for _, l := range s.CategorizerLabels {
		if catIDs[l.ID] {
			probs.add(l.Path, fmt.Errorf("categorizer label id %s already used", l.ID))
		}
		catIDs[l.ID] = true
	}
	caseIDs := map[string]bool{}
	for _, c := range s.Adversarial {
		if caseIDs[c.ID] {
			probs.add(c.Path, fmt.Errorf("adversarial case id %s already used", c.ID))
		}
		caseIDs[c.ID] = true
		for _, id := range c.Rules {
			if _, ok := ids[id]; !ok {
				probs.add(c.Path, fmt.Errorf("case names unknown rule %s", id))
			}
		}
	}
}

func parseRule(p string, data []byte, ruleScope string) (Rule, error) {
	var r schema.Rule
	if err := schema.Decode(schema.DocRule, data, &r); err != nil {
		return Rule{}, err
	}
	if path.Base(p) != r.ID+".yaml" {
		return Rule{}, fmt.Errorf("rule id %s does not match file name %s", r.ID, path.Base(p))
	}
	pats, err := scope.CompilePatterns(r.FilePatterns)
	if err != nil {
		return Rule{}, fmt.Errorf("file_patterns: %w", err)
	}
	if r.FixPolicy == "" {
		r.FixPolicy = "none"
	}
	if r.ModelTier == "" {
		r.ModelTier = "standard"
	}
	return Rule{Rule: r, Path: p, Scope: ruleScope, VersionSHA: BlobSHA(data), Patterns: pats}, nil
}

// BlobSHA returns the git blob id of data, matching `git hash-object`.
func BlobSHA(data []byte) string {
	h := sha1.New() //nolint:gosec // git object ids
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func decodeFile(fsys fs.FS, p string, d schema.Document, out any, probs *Problems) {
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		probs.add(p, err)
		return
	}
	if err := schema.Decode(d, data, out); err != nil {
		probs.add(p, err)
	}
}

// walkYAML calls fn for every .yaml file under dir. A missing dir is fine.
func walkYAML(fsys fs.FS, dir string, probs *Problems, fn func(p string, data []byte)) {
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == dir {
				return fs.SkipDir
			}
			return err
		}
		if d.IsDir() || path.Ext(p) != ".yaml" {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			probs.add(p, err)
			return nil
		}
		fn(p, data)
		return nil
	})
	if err != nil {
		probs.add(dir, err)
	}
}
