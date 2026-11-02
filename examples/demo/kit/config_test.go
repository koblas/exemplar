package main

import (
	"os"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/koblas/exemplar/internal/config"
)

// TestRenderConfigLoads: the rendered demo config is a valid
// exemplar-config tree, with the demo repo, the demo rule and the global
// switches the beats need, and without the example's own repo files.
func TestRenderConfigLoads(t *testing.T) {
	files, err := renderConfig(os.DirFS("../../exemplar-config"), testVars)
	if err != nil {
		t.Fatal(err)
	}
	mapfs := fstest.MapFS{}
	for p, c := range files {
		mapfs[p] = &fstest.MapFile{Data: []byte(c)}
	}
	snap, err := config.LoadSnapshot(mapfs, "demo")
	if err != nil {
		t.Fatalf("rendered config does not load: %v", err)
	}
	if _, ok := files["repos/exemplar-sandbox/playground.yaml"]; ok {
		t.Error("the example's sandbox repo file is still in the tree")
	}
	rc, ok := snap.Repos[testVars.Org+"/"+testVars.Repo]
	if !ok {
		t.Fatalf("no repo file for %s/%s; repos: %v", testVars.Org, testVars.Repo, sortedKeys(snap.Repos))
	}
	if rc.Shadow || rc.AutoApprove == nil || !rc.AutoApprove.Enabled || len(rc.AutoApprove.AllowedAuthors) != 1 || rc.AutoApprove.AllowedAuthors[0] != testVars.Author {
		t.Errorf("repo config = %+v", rc)
	}
	g := snap.Global
	if !g.AutoApprove || g.Baseline.SampleRate != 1 {
		t.Errorf("global auto_approve %v, baseline %v", g.AutoApprove, g.Baseline.SampleRate)
	}
	if c := g.Providers.Categorizer; c == nil || c.A.Provider != "claude" || c.B.Provider != "openai" {
		t.Errorf("categorizer = %+v, want claude and openai", c)
	}
	if v := g.Providers.Verifier; len(v) == 0 || v[0].Provider != "openai" {
		t.Errorf("verifier = %+v, want openai first", v)
	}
	var rule *config.Rule
	for i := range snap.Rules {
		if snap.Rules[i].ID == DemoRule {
			rule = &snap.Rules[i]
		}
	}
	if rule == nil {
		t.Fatalf("no %s rule", DemoRule)
	}
	if rule.Owner != "@"+testVars.Owner || rule.Status != "active" || len(rule.FilePatterns) != 1 {
		t.Errorf("demo rule owner %s, status %s, file_patterns %v", rule.Owner, rule.Status, rule.FilePatterns)
	}
	cases, ok := snap.Evals[DemoRule]
	if !ok {
		t.Fatalf("no %s", path.Join("evals", DemoRule, "cases.yaml"))
	}
	if n := len(cases.Cases); n < g.EvalThresholds.MinCases {
		t.Errorf("%d eval cases, below eval_thresholds.min_cases %d", n, g.EvalThresholds.MinCases)
	}
	// Each synthetic patch's context must be the base file's lines 21-23.
	base, err := tree("_repo", testVars)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(base["internal/payments/payments.go"], "\n")
	for _, c := range cases.Cases {
		var ctx []string
		for l := range strings.SplitSeq(c.Patch, "\n") {
			if strings.HasPrefix(l, " ") {
				ctx = append(ctx, l[1:])
			}
		}
		if !slices.Equal(ctx, lines[20:23]) {
			t.Errorf("case %s: patch context %q does not match the base file %q", c.Name, ctx, lines[20:23])
		}
	}
	if g.Verifier.ImportanceMin != DemoImportanceMin {
		t.Errorf("verifier.importance_min = %v", g.Verifier.ImportanceMin)
	}
	if !strings.Contains(files["CODEOWNERS"], "@"+testVars.Owner) {
		t.Errorf("exemplar-config CODEOWNERS does not name the demo owner: %q", files["CODEOWNERS"])
	}

	// The product's own gate: the demo repo is reviewed, with the demo rule.
	repo := testVars.Org + "/" + testVars.Repo
	if err := snap.CheckEligible(repo); err != nil {
		t.Fatalf("%s is not eligible: %v", repo, err)
	}
	eff, err := snap.Effective(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(eff.Rules, func(r config.Rule) bool { return r.ID == DemoRule }) {
		t.Errorf("%s does not apply to %s", DemoRule, repo)
	}
}
