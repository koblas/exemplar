package config

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/koblas/exemplar/internal/schema"
)

const recommended = "exemplar:recommended"

// presetOnlyTree is the example config with no rules/ and no evals/,
// extending exemplar:recommended plus extra global.yaml lines.
func presetOnlyTree(t *testing.T, global string) fstest.MapFS {
	t.Helper()
	fsys := baseTree(t)
	for p := range fsys {
		if strings.HasPrefix(p, "rules/") || strings.HasPrefix(p, "evals/") {
			delete(fsys, p)
		}
	}
	appendGlobal(fsys, "extends: ["+recommended+"]\n"+global)
	return fsys
}

func appendGlobal(fsys fstest.MapFS, lines string) {
	data := slices.Clone(fsys[GlobalPath].Data)
	fsys[GlobalPath] = &fstest.MapFile{Data: append(data, lines...)}
}

func ruleByID(rules []Rule, id string) *Rule {
	for i := range rules {
		if rules[i].ID == id {
			return &rules[i]
		}
	}
	return nil
}

func TestExtendsRecommendedWithoutRules(t *testing.T) {
	s, err := LoadSnapshot(presetOnlyTree(t, ""), "v")
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if got := len(s.Rules); got != 9 {
		t.Fatalf("got %d rules, want the 9 in %s", got, recommended)
	}
	var required []string
	for _, r := range s.Rules {
		if !strings.HasPrefix(r.ID, "exemplar.") || r.Scope != recommended || !IsOrgScope(r.Scope) {
			t.Errorf("rule %s: scope %q", r.ID, r.Scope)
		}
		if r.Required {
			required = append(required, r.ID)
		}
	}
	// Security or blocking: the three security rules.
	want := []string{"exemplar.no-raw-sql-concat", "exemplar.no-secrets-in-logs", "exemplar.untrusted-input-to-sink"}
	if !slices.Equal(required, want) {
		t.Errorf("required = %v, want %v", required, want)
	}
	e, err := s.Effective("exemplar-sandbox/playground", nil)
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if len(e.Rules) != 9 {
		t.Errorf("effective rules = %d, want 9", len(e.Rules))
	}
}

func TestNoExtendsRunsNoPresetRules(t *testing.T) {
	s := loadExample(t)
	if _, ok := s.Presets[recommended]; !ok {
		t.Fatalf("Presets lacks %s: %v", recommended, s.Presets)
	}
	for _, r := range s.Rules {
		if r.Scope != GlobalScope {
			t.Errorf("rule %s from %s runs without extends", r.ID, r.Scope)
		}
	}
}

func TestStarterCopiesCoexistWithPreset(t *testing.T) {
	fsys := baseTree(t)
	appendGlobal(fsys, "extends: ["+recommended+"]\n")
	s, err := LoadSnapshot(fsys, "v")
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if ruleByID(s.Rules, "propagate-context") == nil || ruleByID(s.Rules, "exemplar.propagate-context") == nil {
		t.Errorf("want both the copied and the preset rule; got %d rules", len(s.Rules))
	}
}

func TestRuleSettings(t *testing.T) {
	s, err := LoadSnapshot(presetOnlyTree(t, `disabled_rules: [exemplar.pin-container-images]
rule_settings:
  exemplar.propagate-context: { status: shadow, exclude_paths: ["tools/**"] }
  exemplar.no-raw-sql-concat: { required: false }
  exemplar.wrap-errors-with-cause: { required: true }
`), "v")
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if ruleByID(s.Rules, "exemplar.pin-container-images") != nil {
		t.Error("disabled rule still loaded")
	}
	pc := ruleByID(s.Rules, "exemplar.propagate-context")
	if pc.Status != schema.StatusShadow {
		t.Errorf("propagate-context status = %s, want shadow", pc.Status)
	}
	if pc.Patterns.Match("tools/gen/main.go") || !pc.Patterns.Match("cmd/api/main.go") {
		t.Errorf("exclude_paths not applied: %v", pc.FilePatterns)
	}
	if orig := s.Presets[recommended]; slices.Contains(ruleByID(orig, "exemplar.propagate-context").FilePatterns, "!tools/**") {
		t.Error("setting leaked into the unresolved preset")
	}
	if ruleByID(s.Rules, "exemplar.no-raw-sql-concat").Required {
		t.Error("required: false not applied")
	}
	if !ruleByID(s.Rules, "exemplar.wrap-errors-with-cause").Required {
		t.Error("required: true not applied")
	}
}

func TestShadowSettingNeverRaises(t *testing.T) {
	fsys := baseTree(t)
	rule := strings.Replace(string(fsys["rules/reliability/propagate-context.yaml"].Data), "status: active", "status: killed", 1)
	fsys["rules/reliability/propagate-context.yaml"] = &fstest.MapFile{Data: []byte(rule)}
	appendGlobal(fsys, "rule_settings:\n  propagate-context: { status: shadow }\n")
	s, err := LoadSnapshot(fsys, "v")
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if got := ruleByID(s.Rules, "propagate-context").Status; got != schema.StatusKilled {
		t.Errorf("status = %s, want killed", got)
	}
}

func TestOrgPresets(t *testing.T) {
	fsys := presetOnlyTree(t, "")
	rule := strings.Replace(string(baseTree(t)["rules/reliability/propagate-context.yaml"].Data), "id: propagate-context", "id: payments-context", 1)
	fsys["presets/payments/payments-context.yaml"] = &fstest.MapFile{Data: []byte(rule)}
	fsys["presets/unused/unused-context.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(rule, "id: payments-context", "id: unused-context", 1))}
	appendGlobal(fsys, "")

	s, err := LoadSnapshot(fsys, "v")
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if ruleByID(s.Rules, "payments-context") != nil {
		t.Error("org preset rule runs without extends")
	}
	if len(s.Presets["org:unused"]) != 1 || s.Presets["org:payments"][0].Scope != "org:payments" {
		t.Errorf("org presets not loaded: %v", s.Presets)
	}

	fsys[GlobalPath] = &fstest.MapFile{Data: []byte(strings.Replace(string(fsys[GlobalPath].Data), "extends: ["+recommended+"]", "extends: ["+recommended+", org:payments]", 1))}
	s, err = LoadSnapshot(fsys, "v")
	if err != nil {
		t.Fatalf("LoadSnapshot with org:payments: %v", err)
	}
	r := ruleByID(s.Rules, "payments-context")
	if r == nil || r.Required || r.Path != "presets/payments/payments-context.yaml" {
		t.Errorf("payments-context = %+v", r)
	}
}

func TestLayeringErrors(t *testing.T) {
	propagate := string(baseTree(t)["rules/reliability/propagate-context.yaml"].Data)
	tests := []struct {
		name    string
		edit    func(fstest.MapFS)
		path    string
		wantErr error
	}{
		{"unknown shipped preset", func(f fstest.MapFS) { appendGlobal(f, "extends: [exemplar:nope]\n") }, GlobalPath, ErrUnknownPreset},
		{"unknown org preset", func(f fstest.MapFS) { appendGlobal(f, "extends: [org:nope]\n") }, GlobalPath, ErrUnknownPreset},
		{"reserved id in rules/", func(f fstest.MapFS) {
			f["rules/reliability/exemplar.mine.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(propagate, "id: propagate-context", "id: exemplar.mine", 1))}
		}, "rules/reliability/exemplar.mine.yaml", ErrReservedID},
		{"reserved id in an org preset", func(f fstest.MapFS) {
			f["presets/p/exemplar.mine.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(propagate, "id: propagate-context", "id: exemplar.mine", 1))}
		}, "presets/p/exemplar.mine.yaml", ErrReservedID},
		{"org preset id also in rules/", func(f fstest.MapFS) {
			f["presets/p/propagate-context.yaml"] = &fstest.MapFile{Data: []byte(propagate)}
			appendGlobal(f, "extends: [org:p]\n")
		}, "rules/reliability/propagate-context.yaml", ErrDuplicateID},
		{"disabled_rules unknown", func(f fstest.MapFS) { appendGlobal(f, "disabled_rules: [exemplar.propagate-context]\n") }, GlobalPath, ErrUnknownRule},
		{"rule_settings unknown", func(f fstest.MapFS) {
			appendGlobal(f, "rule_settings:\n  nope: { status: shadow }\n")
		}, GlobalPath, ErrUnknownRule},
		{
			"cases name a disabled rule", func(f fstest.MapFS) {
				f["evals/_adversarial/mine.yaml"] = &fstest.MapFile{Data: []byte(localCopy(t, "adversarial/diff-false-approval.yaml", "mine"))}
				appendGlobal(f, "disabled_rules: [propagate-context]\n")
			},
			"evals/_adversarial/mine.yaml", ErrUnknownRule,
		},
		{"exclude_paths invalid", func(f fstest.MapFS) {
			appendGlobal(f, "rule_settings:\n  propagate-context: { exclude_paths: [\"!x/**\"] }\n")
		}, GlobalPath, ErrInvalidValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := baseTree(t)
			tt.edit(fsys)
			_, err := LoadSnapshot(fsys, "v")
			var probs Problems
			if !errors.As(err, &probs) {
				t.Fatalf("LoadSnapshot = %v, want Problems", err)
			}
			for _, p := range probs {
				if p.Path == tt.path && errors.Is(p.Err, tt.wantErr) {
					return
				}
			}
			t.Errorf("want %v at %s; got:\n%v", tt.wantErr, tt.path, probs)
		})
	}
}

func TestRepoRuleReservedID(t *testing.T) {
	propagate := string(baseTree(t)["rules/reliability/propagate-context.yaml"].Data)
	base := fstest.MapFS{
		RepoRulesDir + "/exemplar.mine.yaml": {Data: []byte(strings.Replace(propagate, "id: propagate-context", "id: exemplar.mine", 1))},
	}
	_, err := LoadRepoRules(base, "acme/api")
	var probs Problems
	if !errors.As(err, &probs) || len(probs) != 1 || !errors.Is(probs[0].Err, ErrReservedID) {
		t.Fatalf("LoadRepoRules = %v, want ErrReservedID", err)
	}
}

func TestRuleOwners(t *testing.T) {
	fsys := presetOnlyTree(t, `preset_owner: "@acme/platform"
rule_settings:
  exemplar.no-secrets-in-logs: { owner: "@acme/security" }
`)
	rule := strings.Replace(string(baseTree(t)["rules/reliability/propagate-context.yaml"].Data), "id: propagate-context", "id: payments-context", 1)
	fsys["presets/payments/payments-context.yaml"] = &fstest.MapFile{Data: []byte(rule)}
	fsys[GlobalPath] = &fstest.MapFile{Data: []byte(strings.Replace(string(fsys[GlobalPath].Data), "extends: ["+recommended+"]", "extends: ["+recommended+", org:payments]", 1))}
	s, err := LoadSnapshot(fsys, "v")
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	for id, want := range map[string]string{
		"exemplar.no-secrets-in-logs": "@acme/security",                                              // rule_settings owner
		"exemplar.propagate-context":  "@acme/platform",                                              // preset_owner
		"payments-context":            ruleByID(s.Presets["org:payments"], "payments-context").Owner, // an org preset keeps its own
	} {
		if got := ruleByID(s.Rules, id).Owner; got != want {
			t.Errorf("%s: owner %s, want %s", id, got, want)
		}
	}
	if got := ruleByID(s.Presets[recommended], "exemplar.propagate-context").Owner; got == "@acme/platform" {
		t.Error("preset_owner leaked into the unresolved preset")
	}
}
