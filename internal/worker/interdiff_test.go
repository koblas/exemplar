package worker

import (
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/store/queries"
)

func scopedRule(id, version, lane string, files ...string) engine.ScopedRule {
	r := config.Rule{VersionSHA: version}
	r.ID = id
	return engine.ScopedRule{Rule: r, Lane: lane, Files: files}
}

func prevRun(id, version, lane, status string, cov float32, origin int64, files ...string) queries.CarryableRunsRow {
	return queries.CarryableRunsRow{RuleID: id, RuleVersion: version, Lane: lane, Status: status, Coverage: &cov, OriginID: origin, Candidates: 2, ScopedFiles: files}
}

func TestPlanCarry(t *testing.T) {
	ex := engine.LaneExemplar
	scoped := []engine.ScopedRule{
		scopedRule("carry", "v1", ex, "a.go", "b.go"),
		scopedRule("changed", "v1", ex, "c.go"),
		scopedRule("open", "v1", ex, "d.go"),
		scopedRule("newversion", "v2", ex, "a.go"),
		scopedRule("newfile", "v1", ex, "a.go", "e.go"),
		scopedRule("failed", "v1", ex, "a.go"),
		scopedRule("partial", "v1", ex, "a.go"),
		scopedRule("compiled", "v1", engine.LaneCompiled, "a.go"),
		scopedRule("new", "v1", ex, "a.go"),
	}
	prev := []queries.CarryableRunsRow{
		prevRun("carry", "v1", ex, engine.RunOK, 1, 10, "b.go", "a.go"),
		prevRun("changed", "v1", ex, engine.RunOK, 1, 11, "c.go"),
		prevRun("open", "v1", ex, engine.RunOK, 1, 12, "d.go"),
		prevRun("newversion", "v1", ex, engine.RunOK, 1, 13, "a.go"),
		prevRun("newfile", "v1", ex, engine.RunOK, 1, 14, "a.go"),
		prevRun("failed", "v1", ex, engine.RunSchemaError, 1, 15, "a.go"),
		prevRun("partial", "v1", ex, engine.RunOK, 0.5, 16, "a.go"),
		prevRun("compiled", "v1", engine.LaneCompiled, engine.RunOK, 1, 17, "a.go"),
	}
	got := PlanCarry(scoped, prev, []string{"c.go", "e.go"}, []string{"d.go"})
	if len(got) != 1 || got["carry"] != (engine.CarriedRun{FromRunID: 10, Version: "v1", Candidates: 2}) {
		t.Errorf("carry = %+v", got)
	}
	if len(PlanCarry(scoped, nil, nil, nil)) != 0 {
		t.Error("carried with no previous review")
	}
}
