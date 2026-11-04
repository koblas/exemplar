package config

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/schema"
)

func shadow(s *schema.RuleSetting) { s.Status = schema.StatusShadow }

func TestEditRuleSetting(t *testing.T) {
	head := string(baseTree(t)[GlobalPath].Data)
	tail := "\nextends: [exemplar:recommended]  # kept as written\n"
	tests := []struct {
		name, in, id string
		fn           func(*schema.RuleSetting)
		want         string
	}{
		{"no rule_settings", head, "exemplar.x", shadow, head + "rule_settings:\n  exemplar.x: { status: shadow }\n"},
		{"empty flow map", head + "rule_settings: {}\n" + tail, "exemplar.x", shadow, head + "rule_settings:\n  exemplar.x: { status: shadow }\n" + tail},
		{
			"new entry after others", head + "rule_settings:\n  # pinned by security\n  exemplar.a: { required: true }\n" + tail, "exemplar.x", shadow,
			head + "rule_settings:\n  # pinned by security\n  exemplar.a: { required: true }\n  exemplar.x: { status: shadow }\n" + tail,
		},
		{
			"flow entry merged", head + "rule_settings:\n  exemplar.x: { exclude_paths: [\"tools/**\"] }\n  exemplar.y: { status: shadow }\n", "exemplar.x", shadow,
			head + "rule_settings:\n  exemplar.x: { status: shadow, exclude_paths: [\"tools/**\"] }\n  exemplar.y: { status: shadow }\n",
		},
		{
			"block entry replaced", head + "rule_settings:\n    exemplar.x:\n      owner: \"@acme/pay\"\n      exclude_paths:\n        - gen/**\n    exemplar.y: { required: false }\n" + tail, "exemplar.x",
			func(s *schema.RuleSetting) { s.ExcludePaths = append(s.ExcludePaths, "tools/**") },
			head + "rule_settings:\n    exemplar.x: { exclude_paths: [\"gen/**\", \"tools/**\"], owner: \"@acme/pay\" }\n    exemplar.y: { required: false }\n" + tail,
		},
		{
			"last block entry", head + "rule_settings:\n  exemplar.x:\n    owner: \"@acme/pay\"\n" + tail, "exemplar.x", shadow,
			head + "rule_settings:\n  exemplar.x: { status: shadow, owner: \"@acme/pay\" }\n" + tail,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EditRuleSetting([]byte(tt.in), tt.id, tt.fn)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestEditRuleSettingRefuses(t *testing.T) {
	in := string(baseTree(t)[GlobalPath].Data) + "rule_settings: { exemplar.a: { status: shadow } }\n"
	_, err := EditRuleSetting([]byte(in), "exemplar.x", shadow)
	if !errors.Is(err, ErrSettingsLayout) {
		t.Errorf("inline rule_settings: err = %v, want ErrSettingsLayout", err)
	}
}

// TestEditRuleSettingExample edits the example global.yaml: everything but
// the new entry stays byte for byte, and the config still loads with the
// rule in shadow.
func TestEditRuleSettingExample(t *testing.T) {
	fsys := presetOnlyTree(t, "")
	before := fsys[GlobalPath].Data
	got, err := EditRuleSetting(before, "exemplar.propagate-context", shadow)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, before) {
		t.Errorf("edit changed existing lines:\n%s", got)
	}
	fsys[GlobalPath].Data = got
	s, err := LoadSnapshot(fsys, "v")
	if err != nil {
		t.Fatal(err)
	}
	if r := ruleByID(s.Rules, "exemplar.propagate-context"); r.Status != schema.StatusShadow {
		t.Errorf("status = %s, want shadow", r.Status)
	}
	// A second edit on the result, as a later PR from the same base
	// re-applied on top would make.
	again, err := EditRuleSetting(got, "exemplar.propagate-context", func(s *schema.RuleSetting) { s.ExcludePaths = []string{"tools/**"} })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(again), "exemplar.propagate-context:") != 1 {
		t.Errorf("entry duplicated:\n%s", again)
	}
}
