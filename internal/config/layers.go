package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/koblas/exemplar/internal/presets"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
)

// Layering failures (spec §3.1).
var (
	// ErrUnknownPreset marks an extends entry that names no preset.
	ErrUnknownPreset = errors.New("unknown preset")
	// ErrReservedID marks a rule outside a shipped preset whose id uses the
	// reserved exemplar. prefix.
	ErrReservedID = errors.New("reserved rule id")
)

// PresetsDir holds the config repo's own presets, one directory each,
// named in extends as org:<dir>.
const PresetsDir = "presets"

// GlobalScope is the scope of a rule from the config repo's rules/.
const GlobalScope = "global"

// IsOrgScope reports whether a rule scope belongs to the organization (rules/
// or a preset) rather than one target repo.
func IsOrgScope(s string) bool {
	return s == GlobalScope || strings.HasPrefix(s, presets.RefPrefix) || strings.HasPrefix(s, orgRefPrefix)
}

const orgRefPrefix = "org:"

// Shipped reports whether r comes from a preset embedded in the binary.
// It has no file in the config repo: the learning plane changes it
// through global.yaml's rule_settings.
func (r Rule) Shipped() bool { return strings.HasPrefix(r.Scope, presets.RefPrefix) }

// ConfigFile is the config repo file a change to r goes in: its own file,
// or global.yaml (rule_settings) for a shipped preset rule.
func (r Rule) ConfigFile() string {
	if r.Shipped() {
		return GlobalPath
	}
	return r.Path
}

// loadPresets reads every preset a config may extend: the shipped ones and
// presets/<name>/ in fsys. Each rule's scope is its preset reference.
func loadPresets(fsys fs.FS, probs *Problems) map[string][]Rule {
	out := map[string][]Rule{}
	shipped, err := fs.ReadDir(presets.FS, ".")
	if err != nil {
		probs.add("presets", err)
	}
	for _, d := range shipped {
		ref := presets.RefPrefix + d.Name()
		sub, err := fs.Sub(presets.FS, d.Name())
		if err != nil {
			probs.add(ref, err)
			continue
		}
		out[ref] = loadPresetRules(sub, ".", ref, ref, probs)
	}
	dirs, err := fs.ReadDir(fsys, PresetsDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		probs.add(PresetsDir, err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		ref := orgRefPrefix + d.Name()
		rules := loadPresetRules(fsys, path.Join(PresetsDir, d.Name()), "", ref, probs)
		for _, r := range rules {
			if err := checkReserved(r); err != nil {
				probs.add(r.Path, err)
			}
		}
		out[ref] = rules
	}
	return out
}

// loadPresetRules parses the rules under dir. Paths are reported under
// label when set (a shipped preset has no path in the config repo).
func loadPresetRules(fsys fs.FS, dir, label, ref string, probs *Problems) []Rule {
	var rules []Rule
	ids := map[string]string{}
	walkYAML(fsys, dir, probs, func(p string, data []byte) {
		if label != "" {
			p = label + "/" + p
		}
		r, err := parseRule(p, data, ref)
		if err != nil {
			probs.add(p, err)
			return
		}
		if prev, dup := ids[r.ID]; dup {
			probs.add(p, fmt.Errorf("%w: rule id %s already defined in %s", ErrDuplicateID, r.ID, prev))
			return
		}
		ids[r.ID] = p
		rules = append(rules, r)
	})
	return rules
}

// resolve builds s.Rules for the organization layer: the rules of every
// extended preset plus rules/, minus disabled_rules, with rule_settings
// applied. It returns every id defined before disabling, by path, for
// checks on references to rules.
func (s *Snapshot) resolve(orgRules []Rule, probs *Problems) map[string]string {
	g := s.Global
	defined := map[string]string{}
	var all []Rule
	add := func(r Rule) {
		if prev, dup := defined[r.ID]; dup {
			probs.add(r.Path, fmt.Errorf("%w: rule id %s already defined in %s", ErrDuplicateID, r.ID, prev))
			return
		}
		defined[r.ID] = r.Path
		all = append(all, r)
	}
	for _, ref := range g.Extends {
		rules, ok := s.Presets[ref]
		if !ok {
			probs.add(GlobalPath, fmt.Errorf("%w: extends names %s", ErrUnknownPreset, ref))
			continue
		}
		for _, r := range rules {
			r.Required = r.Category == "Security" || r.Severity == "blocking"
			// The shipped files name placeholder teams; an org preset's
			// owners are the org's own.
			if g.PresetOwner != "" && r.Shipped() {
				r.Owner = g.PresetOwner
			}
			add(r)
		}
	}
	for _, r := range orgRules {
		add(r)
	}
	for _, id := range g.DisabledRules {
		if _, ok := defined[id]; !ok {
			probs.add(GlobalPath, fmt.Errorf("%w: disabled_rules names %s, which no extended preset or rules/ defines", ErrUnknownRule, id))
		}
	}
	for id := range g.RuleSettings {
		if _, ok := defined[id]; !ok {
			probs.add(GlobalPath, fmt.Errorf("%w: rule_settings names %s, which no extended preset or rules/ defines", ErrUnknownRule, id))
		}
	}
	s.Rules = nil
	for _, r := range all {
		if slices.Contains(g.DisabledRules, r.ID) {
			continue
		}
		if set, ok := g.RuleSettings[r.ID]; ok {
			var err error
			if r, err = applySetting(r, set); err != nil {
				probs.add(GlobalPath, fmt.Errorf("rule_settings.%s: %w", r.ID, err))
			}
		}
		s.Rules = append(s.Rules, r)
	}
	return defined
}

// applySetting narrows r: a shadow status lowers an active rule, and
// exclude_paths become negated file_patterns. An owner replaces the
// rule's.
func applySetting(r Rule, set schema.RuleSetting) (Rule, error) {
	if set.Owner != "" {
		r.Owner = set.Owner
	}
	if set.Status == schema.StatusShadow && r.Status == schema.StatusActive {
		r.Status = schema.StatusShadow
	}
	if set.Required != nil {
		r.Required = *set.Required
	}
	if len(set.ExcludePaths) == 0 {
		return r, nil
	}
	pats := slices.Clone(r.FilePatterns)
	for _, g := range set.ExcludePaths {
		if strings.HasPrefix(g, "!") || !doublestar.ValidatePattern(g) {
			return r, fmt.Errorf("%w: exclude_paths pattern %q is invalid", ErrInvalidValue, g)
		}
		pats = append(pats, "!"+g)
	}
	compiled, err := scope.CompilePatterns(pats)
	if err != nil {
		return r, fmt.Errorf("exclude_paths: %w", err)
	}
	r.FilePatterns, r.Patterns = pats, compiled
	return r, nil
}

// checkReserved refuses the shipped presets' id prefix on a rule that is
// not from one.
func checkReserved(r Rule) error {
	if strings.HasPrefix(r.ID, presets.Prefix) {
		return fmt.Errorf("%w: %s: the %s prefix is reserved for shipped presets", ErrReservedID, r.ID, presets.Prefix)
	}
	return nil
}
