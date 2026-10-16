package pipeline

import (
	"encoding/json"
	"testing"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/scope"
)

func TestBuildSARIF(t *testing.T) {
	at := func(path string, side scope.Side, line int) scope.Anchor {
		return scope.Anchor{Path: path, Side: side, StartLine: line, EndLine: line + 1}
	}
	res := &engine.Result{Findings: []engine.Finding{
		{
			RuleID: "no-raw-sql", Severity: "blocking", Category: "Security", Anchor: at("db.go", scope.SideRight, 4), Message: "Use placeholders.",
			Fingerprint: "fp1", State: engine.StatePosted, Citation: "https://docs/sql",
		},
		{RuleID: "no-raw-sql", Severity: "blocking", Category: "Security", Anchor: at("db.go", scope.SideRight, 9), Fingerprint: "fp2", State: engine.StateDropped, Suppressed: engine.SuppressAlreadyPosted},
		{RuleID: "weak-hash", Severity: "important", Category: "security", Anchor: at("h.go", scope.SideRight, 1), Fingerprint: "fp3", State: engine.StatePosted},
		{RuleID: "no-raw-sql", Severity: "blocking", Category: "Security", Anchor: at("db.go", scope.SideLeft, 2), State: engine.StatePosted}, // removed lines
		{RuleID: "no-raw-sql", Severity: "blocking", Category: "Security", Anchor: at("db.go", scope.SideRight, 20), State: engine.StateShadow},
		{RuleID: "propagate-context", Severity: "important", Category: "Reliability", Anchor: at("a.go", scope.SideRight, 3), State: engine.StatePosted},
	}}
	fs := sarifFindings(res)
	if len(fs) != 3 {
		t.Fatalf("sarifFindings = %d: %+v", len(fs), fs)
	}
	raw, err := BuildSARIF(fs)
	if err != nil {
		t.Fatal(err)
	}
	var log struct {
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name  string `json:"name"`
					Rules []struct {
						ID         string            `json:"id"`
						HelpURI    string            `json:"helpUri"`
						Default    map[string]string `json:"defaultConfiguration"`
						Properties map[string]any    `json:"properties"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			AutomationDetails map[string]string `json:"automationDetails"`
			Results           []struct {
				RuleID              string            `json:"ruleId"`
				Level               string            `json:"level"`
				PartialFingerprints map[string]string `json:"partialFingerprints"`
				Locations           []struct {
					Physical struct {
						Artifact struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine int `json:"startLine"`
							EndLine   int `json:"endLine"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(raw, &log); err != nil {
		t.Fatal(err)
	}
	run := log.Runs[0]
	if log.Version != "2.1.0" || run.Tool.Driver.Name != SARIFTool || run.AutomationDetails["id"] != "exemplar/" {
		t.Errorf("log = %s", raw)
	}
	if rules := run.Tool.Driver.Rules; len(rules) != 2 || rules[0].ID != "no-raw-sql" || rules[0].HelpURI != "https://docs/sql" ||
		rules[0].Default["level"] != "error" || rules[0].Properties["security-severity"] != "8.0" || rules[1].Default["level"] != "warning" {
		t.Errorf("rules = %+v", rules)
	}
	r := run.Results[0]
	if len(run.Results) != 3 || r.PartialFingerprints[FingerprintKey] != "fp1" || r.Locations[0].Physical.Artifact.URI != "db.go" ||
		r.Locations[0].Physical.Region.StartLine != 4 || r.Locations[0].Physical.Region.EndLine != 5 || run.Results[1].PartialFingerprints[FingerprintKey] != "fp2" {
		t.Errorf("results = %+v", run.Results)
	}
	// No findings: a valid, empty analysis (it closes earlier alerts).
	if raw, err := BuildSARIF(nil); err != nil || !json.Valid(raw) || string(raw) == "" {
		t.Errorf("empty = %s, %v", raw, err)
	}
}
