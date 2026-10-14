package evalrun

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
)

// EvaluateRule evaluates one rule of snap on its cases in cfg. Rules that do not run (killed, candidate),
// bug classes, and rules without cases fail the gate with a reason.
func EvaluateRule(ctx context.Context, ev *Evaluator, snap *config.Snapshot, cfg fs.FS, id string) (Report, error) {
	var r *config.Rule
	for i := range snap.Rules {
		if snap.Rules[i].ID == id {
			r = &snap.Rules[i]
		}
	}
	if r == nil {
		// A bug class: a built-in rule over every file, with its own cases.
		if bl := snap.Global.BugLane; bl != nil {
			if f, ok := bl.Classes[id]; ok {
				status := schema.StatusActive
				if !f.Enabled {
					status = schema.StatusKilled
				}
				r = &config.Rule{Rule: schema.Rule{ID: id, Kind: schema.KindBugClass, Status: status, FilePatterns: []string{"**"}}}
			}
		}
	}
	if r == nil {
		return Report{Rule: id, Reasons: []string{"rule deleted or not found; nothing to evaluate"}, Pass: true}, nil
	}
	rep := Report{Rule: id, Kind: r.Kind}
	ec, ok := snap.Evals[id]
	switch {
	case r.Status != schema.StatusActive && r.Status != schema.StatusShadow:
		rep.Pass, rep.Reasons = true, []string{"status " + r.Status + " does not run; not evaluated"}
		return rep, nil
	case !ok:
		rep.Reasons = []string{"no evals/" + id + "/cases.yaml"}
		return rep, nil
	case (r.Kind == schema.KindExemplar || r.Kind == schema.KindBugClass) && ev.Engine == nil:
		rep.Reasons = []string{"exemplar rules need a model: set --gateway-url and --gateway-token (or --scripted)"}
		return rep, nil
	}
	cases, err := Cases(cfg, ec)
	if err != nil {
		return rep, err
	}
	rep, err = ev.Rule(ctx, *r, cases)
	if err != nil {
		return rep, err
	}
	Gate(&rep, snap.Global.EvalThresholds)
	return rep, nil
}

// ChangedRules maps changed paths to rule ids: rule files by path, and
// anything under evals/<id>/.
func ChangedRules(snap *config.Snapshot, changed []string) []string {
	var ids []string
	for _, p := range changed {
		for _, r := range snap.Rules {
			if r.Path == p {
				ids = append(ids, r.ID)
			}
		}
		if rest, ok := strings.CutPrefix(p, "evals/"); ok {
			if id, _, ok := strings.Cut(rest, "/"); ok && !strings.HasPrefix(id, "_") {
				ids = append(ids, id)
			}
		}
		if strings.HasPrefix(p, "rules/") && path.Ext(p) == ".yaml" && !slices.ContainsFunc(snap.Rules, func(r config.Rule) bool { return r.Path == p }) {
			ids = append(ids, strings.TrimSuffix(path.Base(p), ".yaml")) // deleted rule
		}
	}
	return ids
}

// Markdown is the eval check's summary table.
func Markdown(reports []Report, t schema.EvalThresholds) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Exemplar eval\n\nThresholds: at least %d cases, precision ≥ %.2f, recall ≥ %.2f.\n\n", t.MinCases, t.Precision, t.Recall)
	b.WriteString("| Rule | Kind | Result | Historical P / R / F1 (n) | Synthetic P / R / F1 (n) | Cost |\n|---|---|---|---|---|---|\n")
	for _, r := range reports {
		res := "✅ pass"
		if !r.Pass {
			res = "❌ " + strings.Join(r.Reasons, "; ")
		} else if len(r.Reasons) > 0 {
			res = "➖ " + strings.Join(r.Reasons, "; ")
		}
		m := func(x Metrics) string {
			if x.Cases == 0 {
				return "—"
			}
			return fmt.Sprintf("%.2f / %.2f / %.2f (%d)", x.Precision, x.Recall, x.F1, x.Cases)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | $%.4f |\n", r.Rule, r.Kind, res, m(r.Historical), m(r.Synthetic), r.CostUSD)
	}
	unavailable := 0
	for _, r := range reports {
		for _, o := range r.Outcomes {
			if o.Result == "unavailable" {
				unavailable++
			}
		}
	}
	if unavailable > 0 {
		fmt.Fprintf(&b, "\n%d %s could not be scored (unreadable files, ineligible repo, or no matching files); see the JSON report.\n", unavailable, plural(unavailable, "case", "cases"))
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
