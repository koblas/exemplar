package gates

import (
	"slices"
	"testing"
)

func TestWeights(t *testing.T) {
	w, err := Weights(map[string]float64{TermChurn: 0, TermIncident: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if w[TermChurn] != 0 || w[TermIncident] != 0.5 || w[TermRevert] != DefaultWeights[TermRevert] {
		t.Errorf("Weights = %v", w)
	}
	for _, bad := range []map[string]float64{{"incident_90d": 0.3}, {TermNoTests: 1.5}, {TermNoTests: -0.1}} {
		if _, err := Weights(bad); err == nil {
			t.Errorf("Weights(%v) accepted", bad)
		}
	}
}

func TestMergeSafety(t *testing.T) {
	w, _ := Weights(nil)
	tests := []struct {
		name  string
		in    MergeSafetyInput
		score float64
		worst string
		terms []string
	}{
		{"clean", MergeSafetyInput{Paths: []PathFacts{{Path: "a.go"}}}, 1, "", nil},
		{
			"worst path only",
			MergeSafetyInput{Paths: []PathFacts{
				{Path: "a.go", Revert: true},                              // 0.20
				{Path: "db/migrate.sql", Incident: true, Sensitive: true}, // 0.55
				{Path: "b.go", Revert: true, MergedPRs: 11},               // 0.30
			}},
			0.45, "db/migrate.sql",
			[]string{TermIncident, TermSensitive},
		},
		{"churn needs more than ten PRs", MergeSafetyInput{Paths: []PathFacts{{Path: "a.go", MergedPRs: 10}}}, 1, "", nil},
		{
			"PR-level terms",
			MergeSafetyInput{Paths: []PathFacts{{Path: "a.go"}}, NoTests: true, NewAuthor: true, ForcePush: true},
			0.70, "",
			[]string{TermNoTests, TermNewAuthor, TermForcePush},
		},
		{
			"floored at zero",
			MergeSafetyInput{
				Paths:   []PathFacts{{Path: "a.go", Incident: true, Revert: true, MergedPRs: 20, Sensitive: true}},
				NoTests: true, NewAuthor: true, ForcePush: true,
			},
			0, "a.go",
			[]string{TermIncident, TermRevert, TermChurn, TermSensitive, TermNoTests, TermNewAuthor, TermForcePush},
		},
		{
			"float noise does not break a tie",
			MergeSafetyInput{Paths: []PathFacts{{Path: "x.go", Incident: true}, {Path: "y.go", Revert: true, MergedPRs: 11}}},
			0.70, "x.go",
			[]string{TermIncident},
		},
		{
			"tie keeps the first path",
			MergeSafetyInput{Paths: []PathFacts{{Path: "x.go", Revert: true}, {Path: "y.go", Revert: true}}},
			0.80, "x.go",
			[]string{TermRevert},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := MergeSafety(tt.in, w)
			var names []string
			for _, term := range res.Terms {
				names = append(names, term.Name)
			}
			if res.Score != tt.score || res.WorstPath != tt.worst || !slices.Equal(names, tt.terms) {
				t.Errorf("MergeSafety = %.2f worst=%q terms=%v; want %.2f %q %v", res.Score, res.WorstPath, names, tt.score, tt.worst, tt.terms)
			}
		})
	}

	// A zero weight disables a term entirely.
	w0, _ := Weights(map[string]float64{TermForcePush: 0})
	if res := MergeSafety(MergeSafetyInput{ForcePush: true}, w0); res.Score != 1 || len(res.Terms) != 0 {
		t.Errorf("zero weight still applied: %+v", res)
	}
}

func TestNoTests(t *testing.T) {
	tests := []struct {
		paths []string
		want  bool
	}{
		{[]string{"svc/a.go"}, true},
		{[]string{"svc/a.go", "svc/a_test.go"}, false},
		{[]string{"app/models.py", "tests/test_models.py"}, false},
		{[]string{"web/cart.ts", "web/cart.spec.ts"}, false},
		{[]string{"src/main/java/Cart.java", "src/test/java/CartTest.java"}, false},
		{[]string{"README.md", "config/flags.yaml"}, false}, // no code changed
		{[]string{"db/migrations/0042_add.sql"}, true},
		{nil, false},
	}
	for _, tt := range tests {
		if got := NoTests(tt.paths); got != tt.want {
			t.Errorf("NoTests(%v) = %v, want %v", tt.paths, got, tt.want)
		}
	}
	if IsTestFile("contest/main.go") || IsTestFile("latest.go") {
		t.Error("IsTestFile matched a non-test path")
	}
}

func TestSensitive(t *testing.T) {
	globs := []string{"db/migrations/**", "**/auth/**", "deploy/**"}
	for p, want := range map[string]bool{
		"db/migrations/0001.sql": true, "svc/auth/token.go": true, "deploy/prod.yaml": true, "svc/cart.go": false,
	} {
		if got := Sensitive(p, globs); got != want {
			t.Errorf("Sensitive(%q) = %v", p, got)
		}
	}
}
