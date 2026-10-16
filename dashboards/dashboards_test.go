// Package dashboards checks the Grafana boards: valid JSON, unique ids, the
// provisioned datasource, and every panel's SQL runs against a migrated
// database and selects no finding text or code (spec §12.7).
package dashboards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

type board struct {
	UID    string `json:"uid"`
	Title  string `json:"title"`
	Panels []struct {
		Title      string `json:"title"`
		Type       string `json:"type"`
		Datasource struct {
			UID string `json:"uid"`
		} `json:"datasource"`
		Targets []struct {
			RawSQL string `json:"rawSql"`
			Format string `json:"format"`
			Expr   string `json:"expr"`
		} `json:"targets"`
	} `json:"panels"`
}

// expand replaces the Grafana macros the boards use.
var timeFilter = regexp.MustCompile(`\$__timeFilter\(((?:[^()]|\([^()]*\))*)\)`)

func expand(sql string) string {
	return timeFilter.ReplaceAllString(sql, "($1) BETWEEN now() - interval '28 days' AND now()")
}

// Finding text, code, fixes and reasons never reach Grafana. The one
// allowed use of suggested_fix is counting findings that have one.
var forbidden = regexp.MustCompile(`(?i)\b(message|reason_text|anchor_code|evidence|verify_evidence|body|dismissed_comment|spec)\b|suggested_fix(\s+IS\s+NOT\s+NULL)?`)

func TestBoards(t *testing.T) {
	files, _ := filepath.Glob("*.json")
	if len(files) < 6 {
		t.Fatalf("boards = %v", files)
	}
	st := storetest.New(t)
	uids := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var b board
		if err := json.Unmarshal(data, &b); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if b.UID == "" || uids[b.UID] || !strings.HasPrefix(b.Title, "Exemplar: ") {
			t.Errorf("%s: uid %q, title %q", f, b.UID, b.Title)
		}
		uids[b.UID] = true
		for _, p := range b.Panels {
			name := f + " / " + p.Title
			if p.Datasource.UID == "exemplar-metrics" {
				checkPromQL(t, name, p.Targets)
				continue
			}
			if p.Datasource.UID != "exemplar-pg" || len(p.Targets) == 0 {
				t.Errorf("%s: datasource %q, %d targets", name, p.Datasource.UID, len(p.Targets))
			}
			for _, tg := range p.Targets {
				for _, m := range forbidden.FindAllStringSubmatch(tg.RawSQL, -1) {
					if !strings.HasPrefix(strings.ToLower(m[0]), "suggested_fix") || m[2] == "" {
						t.Errorf("%s: selects %q (spec §12.7: aggregates only)", name, m[0])
					}
				}
				if (p.Type == "timeseries") != (tg.Format == "time_series") {
					t.Errorf("%s: a %s panel with %s format", name, p.Type, tg.Format)
				}
				rows, err := st.Pool.Query(t.Context(), expand(tg.RawSQL))
				if err != nil {
					t.Errorf("%s: %v", name, err)
					continue
				}
				fields := rows.FieldDescriptions()
				rows.Close()
				if err := rows.Err(); err != nil {
					t.Errorf("%s: %v", name, err)
				}
				if tg.Format == "time_series" && (len(fields) == 0 || fields[0].Name != "time") {
					t.Errorf("%s: a time series must select \"time\" first", name)
				}
			}
		}
	}
}

// The alert rules: each query runs against a migrated database, returns
// one number, and selects no finding text; each reduces A then
// thresholds it.
func TestAlerts(t *testing.T) {
	data, err := os.ReadFile("provisioning/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Groups []struct {
			Rules []struct {
				UID       string `yaml:"uid"`
				Condition string `yaml:"condition"`
				Data      []struct {
					RefID         string `yaml:"refId"`
					DatasourceUID string `yaml:"datasourceUid"`
					Model         struct {
						RawSQL string `yaml:"rawSql"`
						Type   string `yaml:"type"`
					} `yaml:"model"`
				} `yaml:"data"`
				Labels map[string]string `yaml:"labels"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	st := storetest.New(t)
	n := 0
	for _, g := range f.Groups {
		for _, r := range g.Rules {
			n++
			if r.UID == "" || r.Condition != "C" || len(r.Data) != 3 || r.Labels["team"] == "" {
				t.Errorf("%s: shape", r.UID)
				continue
			}
			a := r.Data[0]
			if a.DatasourceUID != "exemplar-pg" || r.Data[1].Model.Type != "reduce" || r.Data[2].Model.Type != "threshold" {
				t.Errorf("%s: data %+v", r.UID, r.Data)
			}
			if m := forbidden.FindString(a.Model.RawSQL); m != "" {
				t.Errorf("%s: selects %q", r.UID, m)
			}
			var v float64
			if err := st.Pool.QueryRow(t.Context(), a.Model.RawSQL).Scan(&v); err != nil || v != 0 {
				t.Errorf("%s: %v, %v (an empty database must not alert)", r.UID, v, err)
			}
		}
	}
	if n < 3 {
		t.Errorf("rules = %d", n)
	}

	// Seeded: an approval from a degraded review of a head that merged and
	// was reverted fires both rules on it.
	for _, q := range []string{
		`INSERT INTO reviews (id, repo, pr_number, head_sha, base_sha, config_version, caller, status, degraded, started_at, finished_at)
			VALUES (1, 'acme/app', 4, 'h', 'b', 'v', 'production', 'done', true, now(), now())`,
		`INSERT INTO approval_decisions (review_id, stage1, stage2, decision) VALUES (1, '{}', '{}', 'approved')`,
		`INSERT INTO pr_merges (repo, pr_number, head_sha, merged_at) VALUES ('acme/app', 4, 'h', now())`,
		`INSERT INTO pr_links (repo, pr_number, kind, ref, at) VALUES ('acme/app', 4, 'revert', '#9', now())`,
	} {
		if _, err := st.Pool.Exec(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range f.Groups {
		for _, r := range g.Rules {
			want := map[string]float64{"exemplar-auto-approved-failed": 1, "exemplar-degraded-approval": 1, "exemplar-auto-approved-failure-rate": 0}[r.UID]
			var v float64
			if err := st.Pool.QueryRow(t.Context(), r.Data[0].Model.RawSQL).Scan(&v); err != nil || v != want {
				t.Errorf("%s seeded = %v, %v; want %v", r.UID, v, err, want)
			}
		}
	}
}

// promMetric finds the metric names a PromQL expression reads.
var promMetric = regexp.MustCompile(`\bexemplar_[a-z_]+`)

// The OTel instruments, as Prometheus names them (internal/telemetry).
var promNames = map[string]bool{
	"exemplar_model_calls_total": true, "exemplar_model_duration_seconds_bucket": true,
	"exemplar_review_duration_seconds_bucket": true, "exemplar_review_queue_to_post_seconds_bucket": true,
	"exemplar_github_rate_limit_remaining": true, "exemplar_github_requests_total": true,
}

func checkPromQL(t *testing.T, name string, targets []struct {
	RawSQL string `json:"rawSql"`
	Format string `json:"format"`
	Expr   string `json:"expr"`
},
) {
	t.Helper()
	if len(targets) == 0 {
		t.Errorf("%s: no targets", name)
	}
	for _, tg := range targets {
		ms := promMetric.FindAllString(tg.Expr, -1)
		if tg.Expr == "" || len(ms) == 0 {
			t.Errorf("%s: no exemplar metric in %q", name, tg.Expr)
		}
		for _, m := range ms {
			if !promNames[m] {
				t.Errorf("%s: unknown metric %s", name, m)
			}
		}
	}
}
