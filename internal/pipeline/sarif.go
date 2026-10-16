package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// SARIF for Security: Security-category findings are also
// uploaded as code-scanning alerts, so they appear in Security's tools.
// The log is SARIF 2.1.0, written with encoding/json rather than
// go-sarif: the few fields used here do not need a dependency.

// SARIFTool is the driver name alerts carry; code_scanning_alert events
// from other tools are not Exemplar's.
const SARIFTool = "Exemplar"

// sarifCategory separates Exemplar's analyses from other tools' on a ref.
const sarifCategory = "exemplar/"

// FingerprintKey is the partialFingerprints key carrying the finding's
// stable identity, so GitHub keeps one alert per finding
// across pushes.
const FingerprintKey = "exemplarFingerprint/v1"

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool              sarifTool         `json:"tool"`
	AutomationDetails map[string]string `json:"automationDetails"`
	Results           []sarifResult     `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name  string      `json:"name"`
	Rules []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID               string            `json:"id"`
	ShortDescription sarifText         `json:"shortDescription"`
	HelpURI          string            `json:"helpUri,omitempty"`
	Default          map[string]string `json:"defaultConfiguration"`
	Properties       map[string]any    `json:"properties"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID              string            `json:"ruleId"`
	Level               string            `json:"level"`
	Message             sarifText         `json:"message"`
	Locations           []sarifLocation   `json:"locations"`
	PartialFingerprints map[string]string `json:"partialFingerprints"`
}

type sarifLocation struct {
	Physical struct {
		Artifact struct {
			URI string `json:"uri"`
		} `json:"artifactLocation"`
		Region struct {
			StartLine int `json:"startLine"`
			EndLine   int `json:"endLine"`
		} `json:"region"`
	} `json:"physicalLocation"`
}

// sarifFindings are the Security findings present on this head: posted
// now, or posted earlier and still open (already_posted), on head lines.
func sarifFindings(res *engine.Result) []engine.Finding {
	var out []engine.Finding
	for _, f := range res.Findings {
		present := f.State == engine.StatePosted || f.Suppressed == engine.SuppressAlreadyPosted
		if present && strings.EqualFold(f.Category, "Security") && f.Anchor.Side == "RIGHT" {
			out = append(out, f)
		}
	}
	return out
}

func sarifLevel(severity string) (string, string) {
	switch severity {
	case schema.SeverityBlocking:
		return "error", "8.0"
	case schema.SeverityImportant:
		return "warning", "5.0"
	}
	return "note", "2.0"
}

// BuildSARIF writes the SARIF log for fs.
func BuildSARIF(fs []engine.Finding) ([]byte, error) {
	run := sarifRun{
		Tool:              sarifTool{Driver: sarifDriver{Name: SARIFTool, Rules: []sarifRule{}}},
		AutomationDetails: map[string]string{"id": sarifCategory},
		Results:           []sarifResult{},
	}
	seen := map[string]bool{}
	for _, f := range fs {
		level, sev := sarifLevel(f.Severity)
		if !seen[f.RuleID] {
			seen[f.RuleID] = true
			run.Tool.Driver.Rules = append(run.Tool.Driver.Rules, sarifRule{
				ID: f.RuleID, ShortDescription: sarifText{Text: f.RuleID}, HelpURI: f.Citation,
				Default:    map[string]string{"level": level},
				Properties: map[string]any{"tags": []string{"security"}, "security-severity": sev},
			})
		}
		var loc sarifLocation
		loc.Physical.Artifact.URI = f.Anchor.Path
		loc.Physical.Region.StartLine, loc.Physical.Region.EndLine = f.Anchor.StartLine, f.Anchor.EndLine
		run.Results = append(run.Results, sarifResult{
			RuleID: f.RuleID, Level: level, Message: sarifText{Text: f.Message}, Locations: []sarifLocation{loc},
			PartialFingerprints: map[string]string{FingerprintKey: f.Fingerprint},
		})
	}
	slices.SortStableFunc(run.Tool.Driver.Rules, func(a, b sarifRule) int { return strings.Compare(a.ID, b.ID) })
	out, err := json.Marshal(sarifLog{Schema: "https://json.schemastore.org/sarif-2.1.0.json", Version: "2.1.0", Runs: []sarifRun{run}})
	if err != nil {
		return nil, fmt.Errorf("encode sarif: %w", err)
	}
	return out, nil
}

// UploadSARIF uploads this head's Security findings. With none, it still
// uploads an empty analysis when an earlier review of the PR posted one,
// so GitHub closes the alerts the code no longer has; otherwise it does
// nothing. It reports whether it uploaded. A repo without code scanning
// is not an error: the inline comments already carry the findings.
func UploadSARIF(ctx context.Context, st *store.Store, poster *gh.Poster, p *engine.Prepared, res *engine.Result) (bool, error) {
	fs := sarifFindings(res)
	if len(fs) == 0 {
		if st == nil {
			return false, nil
		}
		had, err := st.PRHadSecurityFindings(ctx, queries.PRHadSecurityFindingsParams{Repo: p.PR.Repo, PrNumber: int32(p.PR.Number)})
		if err != nil {
			return false, fmt.Errorf("check earlier security findings: %w", err)
		}
		if !had {
			return false, nil
		}
	}
	log, err := BuildSARIF(fs)
	if err != nil {
		return false, err
	}
	pr, err := gh.ParsePR(p.PR.Repo, p.PR.Number, p.PR.HeadSHA)
	if err != nil {
		return false, fmt.Errorf("parse pr: %w", err)
	}
	if _, err := poster.UploadSARIF(ctx, pr, log); errors.Is(err, gh.ErrCodeScanningUnavailable) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("upload sarif: %w", err)
	}
	return true, nil
}
