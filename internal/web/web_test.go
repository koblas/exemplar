package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/learn/demote"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

type fakeAccess struct {
	readable map[string]bool
	fail     map[string]bool
}

func (a fakeAccess) CanRead(_ context.Context, token, repo string) (bool, error) {
	if token != "tok" {
		return false, errors.New("wrong token")
	}
	if a.fail[repo] {
		return false, errors.New("GitHub is down")
	}
	return a.readable[repo], nil
}

func TestUI(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := st.Pool.QueryRow(ctx, sql+" RETURNING 1", args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	id := func(sql string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := st.Pool.QueryRow(ctx, sql+" RETURNING id", args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	for _, r := range []struct{ id, scope string }{{"no-raw-sql", "global"}, {"app-only", "acme/app"}, {"secret-only", "acme/secret"}} {
		exec(`INSERT INTO rules (rule_id, version_sha, scope, kind, category, severity, status, owner, spec) VALUES ($1, 'v', $2, 'exemplar', 'Security', 'blocking', 'active', `+
			`'@acme/sec', json_build_object('id', $1::text, 'status', 'active'))`, r.id, r.scope)
	}
	finding := func(repo, msg string) int64 {
		rid := id(`INSERT INTO reviews (repo, pr_number, head_sha, base_sha, config_version, caller, status, started_at, finished_at) VALUES ($1, 3, 'abcdef0123456789', 'b', 'v', `+
			`'production', 'done', now(), now())`, repo)
		exec(`INSERT INTO approval_decisions (review_id, stage1, stage2, decision) VALUES ($1, '{"reasons": ["paths_not_auto_approvable"]}', '{}', 'ineligible')`, rid)
		run := id(`INSERT INTO rule_runs (review_id, rule_id, rule_version, lane, scoped_files, status) VALUES ($1, 'no-raw-sql', 'v', 'exemplar', '{a.go}', 'ok')`, rid)
		return id(`INSERT INTO findings (rule_run_id, repo, pr_number, rule_id, lane, severity, cc_label, path, side, start_line, end_line, fingerprint, message, state, gh_comment_id)
			VALUES ($1, $2, 3, 'no-raw-sql', 'exemplar', 'blocking', 'issue', 'db/q.go', 'RIGHT', 7, 7, $3, $4, 'posted', 99)`, run, repo, repo, msg)
	}
	appF := finding("acme/app", "SQL built from <script>alert(1)</script> input.")
	secretF := finding("acme/secret", "Secret repo detail.")
	exec(`INSERT INTO feedback_events (finding_id, kind, actor, role, weight, via, reason_text) VALUES ($1, 'fb_fixed', 'alice', 'author', 1, 'slash', 'fixed it')`, appF)
	run := id(`INSERT INTO cluster_runs (params, status) VALUES ('{}', 'done')`)
	for i, repo := range []string{"acme/app", "acme/secret"} {
		exec(`INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at, comment_url) VALUES ($1, $2, 1, 'a.go', 'go', 'r', 'a', now(), 'u')`, 100+i, repo)
		c := id(`INSERT INTO candidates (ext, title, description, cluster_size, distinct_reviewers, score, score_detail, owner, cluster_hash, last_run_id)
			VALUES ('go', $1, 'd', 5, 3, $2, '{}', '@acme/sec', $3, $4)`, "Candidate from "+repo, 0.9-float64(i)/10, repo, run)
		exec(`INSERT INTO candidate_sources (candidate_id, comment_id) VALUES ($1, $2)`, c, 100+i)
	}
	// A candidate drawn from both repos.
	mixed := id(`INSERT INTO candidates (ext, title, description, cluster_size, distinct_reviewers, score, score_detail, owner, cluster_hash, last_run_id)
		VALUES ('go', 'Candidate from both', 'mixed detail', 5, 3, 0.5, '{}', '@acme/sec', 'mixed', $1)`, run)
	exec(`INSERT INTO candidate_sources (candidate_id, comment_id) VALUES ($1, 100), ($1, 101)`, mixed)
	// An owner seed: its text is a rule file in exemplar-config.
	exec(`INSERT INTO candidates (source_kind, ext, title, description, cluster_size, distinct_reviewers, score, score_detail, owner, status,
		rule_id, rule_path, rule_yaml, seed_author) VALUES ('owner_seed', '*', 'Seed by dana', 'seed detail', 0, 0, 0, '{}', '@acme/sec', 'pr_open',
		'seed', 'rules/seed.yaml', 'id: seed', 'dana')`)
	exec(`INSERT INTO model_calls (stage, provider, model, data_classes, caller, cost_usd) VALUES ('generator', 'anthropic', 'm', '{}', 'production', 1.25), ('generator', ` +
		`'anthropic', 'm', '{}', 'cli', 0.5)`)
	if _, err := st.Pool.Exec(ctx, `REFRESH MATERIALIZED VIEW rule_weekly_stats`); err != nil {
		t.Fatal(err)
	}

	access := fakeAccess{readable: map[string]bool{"acme/app": true}, fail: map[string]bool{}}
	signedIn := true
	var acted []string
	var flagged []int64
	read := func(ctx context.Context, repo string) (bool, error) { return access.CanRead(ctx, "tok", repo) }
	h, err := New(Config{
		Store:   st,
		Session: func(*http.Request) (string, Access, bool) { return "alice", read, signedIn },
		SignOut: func(http.ResponseWriter) {},
		Act: func(ctx context.Context, action, target, actor, reason string) (int64, error) {
			acted = append(acted, action+" "+target+" "+actor+" "+reason)
			return st.CreateOwnerAction(ctx, queries.CreateOwnerActionParams{Action: action, Target: target, Actor: actor, Reason: reason})
		},
		FalsePositive: func(_ context.Context, id int64, actor, _ string) error {
			if actor != "alice" {
				t.Errorf("actor = %s", actor)
			}
			flagged = append(flagged, id)
			return nil
		},
		Try: func(ctx context.Context, actor, repo string, pr int, ruleID, y string) (int64, error) {
			return st.CreatePlaygroundRun(ctx, queries.CreatePlaygroundRunParams{Actor: actor, Repo: repo, PrNumber: int32(pr), RuleID: ruleID, RuleYaml: y})
		},
		PlaygroundDailyRuns: 2,
		FormKey:             []byte(strings.Repeat("k", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(path string) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	has := func(t *testing.T, body string, want []string, not []string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("missing %q", w)
			}
		}
		for _, n := range not {
			if strings.Contains(body, n) {
				t.Errorf("shows %q", n)
			}
		}
	}

	t.Run("rules", func(t *testing.T) {
		code, body, hd := get("/ui/")
		if code != 200 || !strings.Contains(hd.Get("Content-Security-Policy"), "default-src 'none'") {
			t.Fatalf("code %d, headers %v", code, hd)
		}
		has(t, body, []string{"no-raw-sql", "app-only", "@acme/sec"}, []string{"secret-only"})
		_, body, _ = get("/ui/?q=app")
		has(t, body, []string{"app-only"}, []string{">no-raw-sql<"})
	})
	t.Run("rule detail", func(t *testing.T) {
		code, body, _ := get("/ui/rules/no-raw-sql")
		if code != 200 {
			t.Fatal(code)
		}
		has(t, body, []string{"acme/app#3", "db/q.go:7", "accepted", "1 more in repos you cannot read"}, []string{"acme/secret#3"})
		if code, _, _ := get("/ui/rules/secret-only"); code != 404 {
			t.Errorf("a secret repo's rule = %d", code)
		}
		if code, _, _ := get("/ui/rules/nope"); code != 404 {
			t.Errorf("missing rule = %d", code)
		}
	})
	t.Run("finding", func(t *testing.T) {
		code, body, _ := get("/ui/findings/" + itoa(appF))
		if code != 200 {
			t.Fatal(code)
		}
		has(t, body, []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "fixed it", "#discussion_r99"}, []string{"<script>alert"})
		if code, body, _ := get("/ui/findings/" + itoa(secretF)); code != 404 || strings.Contains(body, "Secret repo detail") {
			t.Errorf("unreadable finding = %d", code)
		}
		if code, _, _ := get("/ui/findings/x"); code != 404 {
			t.Errorf("bad id = %d", code)
		}
	})
	t.Run("candidates", func(t *testing.T) {
		_, body, _ := get("/ui/candidates")
		// A candidate's text is drawn from its source comments: shown only
		// to viewers who can read every one of their repos.
		// An owner seed is shown to every viewer.
		has(t, body, []string{"Candidate from acme/app", "text hidden", "Seed by dana", "owner seed by @dana"},
			[]string{"Candidate from acme/secret", "Candidate from both", "mixed detail", "acme/secret"})
	})
	t.Run("audit", func(t *testing.T) {
		_, body, _ := get("/ui/audit")
		has(t, body, []string{"acme/app#3", "paths_not_auto_approvable"}, []string{"acme/secret"})
	})
	t.Run("cost", func(t *testing.T) {
		_, body, _ := get("/ui/cost?days=7")
		has(t, body, []string{"$1.75 over the last 7 days", "production", "cli"}, nil)
	})
	t.Run("access failure hides and says so", func(t *testing.T) {
		access.fail["acme/app"] = true
		defer delete(access.fail, "acme/app")
		_, body, _ := get("/ui/audit")
		has(t, body, []string{"could not be checked"}, []string{"acme/app#3"})
	})
	post := func(path string, form url.Values) (int, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode, resp.Header.Get("Location")
	}
	tokenRE := regexp.MustCompile(`name="action" value="([a-z_]+)"><input type="hidden" name="target" value="([^"]+)"><input type="hidden" name="t" value="([^"]+)"`)
	formFor := func(page, action string) url.Values {
		t.Helper()
		_, body, _ := get(page)
		for _, m := range tokenRE.FindAllStringSubmatch(body, -1) {
			if m[1] == action {
				return url.Values{"action": {m[1]}, "target": {m[2]}, "t": {m[3]}}
			}
		}
		t.Fatalf("no %s form on %s", action, page)
		return nil
	}
	t.Run("owner actions", func(t *testing.T) {
		f := formFor("/ui/rules/no-raw-sql", "shadow_rule")
		f.Set("reason", "too noisy")
		if code, loc := post("/ui/actions", f); code != http.StatusSeeOther || loc != "/ui/actions" {
			t.Fatalf("shadow = %d %s", code, loc)
		}
		if len(acted) != 1 || acted[0] != "shadow_rule no-raw-sql alice too noisy" {
			t.Errorf("acted = %v", acted)
		}
		// A token for one rule does not work for another, or another action.
		forged := url.Values{"action": {"kill_rule"}, "target": {"no-raw-sql"}, "t": {f.Get("t")}}
		if code, _ := post("/ui/actions", forged); code != http.StatusForbidden {
			t.Errorf("forged = %d", code)
		}
		forged = url.Values{"action": {"shadow_rule"}, "target": {"app-only"}, "t": {f.Get("t")}}
		if code, _ := post("/ui/actions", forged); code != http.StatusForbidden {
			t.Errorf("other target = %d", code)
		}
		if code, _ := post("/ui/actions", url.Values{"action": {"drop_db"}, "target": {"x"}, "t": {"x"}}); code != http.StatusForbidden {
			t.Errorf("unknown action = %d", code)
		}
		k := formFor("/ui/audit", "kill_switch")
		if code, _ := post("/ui/actions", k); code != http.StatusSeeOther || !strings.HasPrefix(acted[len(acted)-1], "kill_switch acme/app alice") {
			t.Errorf("kill switch = %d %v", code, acted)
		}
		// A valid token for a repo the viewer can no longer read.
		access.readable["acme/app"] = false
		if code, _ := post("/ui/actions", k); code != http.StatusNotFound {
			t.Errorf("unreadable repo kill switch = %d", code)
		}
		access.readable["acme/app"] = true
		c := formFor("/ui/candidates", "kill_candidate")
		c.Set("reason", "duplicate")
		if code, _ := post("/ui/actions", c); code != http.StatusSeeOther {
			t.Errorf("kill candidate = %d", code)
		}
		_, body, _ := get("/ui/actions")
		has(t, body, []string{"shadow_rule", "no-raw-sql", "too noisy", "kill_switch", "kill_candidate", "queued"}, nil)
	})
	t.Run("false positive", func(t *testing.T) {
		_, body, _ := get("/ui/findings/" + itoa(appF))
		m := regexp.MustCompile(`false-positive"><input type="hidden" name="t" value="([^"]+)"`).FindStringSubmatch(body)
		if m == nil {
			t.Fatal("no false-positive form")
		}
		if code, loc := post("/ui/findings/"+itoa(appF)+"/false-positive", url.Values{"t": {m[1]}, "text": {"not SQL"}}); code != http.StatusSeeOther || !strings.Contains(loc, "recorded=1") {
			t.Errorf("false positive = %d %s", code, loc)
		}
		if len(flagged) != 1 || flagged[0] != appF {
			t.Errorf("flagged = %v", flagged)
		}
		if code, _ := post("/ui/findings/"+itoa(secretF)+"/false-positive", url.Values{"t": {m[1]}}); code != http.StatusForbidden {
			t.Errorf("token reused on another finding = %d", code)
		}
		_, body, _ = get("/ui/findings/" + itoa(appF) + "?recorded=1")
		has(t, body, []string{"Recorded as a false positive"}, nil)
	})
	t.Run("playground", func(t *testing.T) {
		_, body, _ := get("/ui/playground?rule=no-raw-sql")
		has(t, body, []string{"id: no-raw-sql", "status: active"}, nil)
		if _, body, _ := get("/ui/playground?rule=secret-only"); strings.Contains(body, "id: secret-only") {
			t.Error("a secret repo's rule was prefilled")
		}
		tok := regexp.MustCompile(`action="/ui/playground"><input type="hidden" name="t" value="([^"]+)"`).FindStringSubmatch(body)
		if tok == nil {
			t.Fatal("no playground form")
		}
		rule, err := os.ReadFile("../../examples/exemplar-config/rules/security/no-raw-sql-concat.yaml")
		if err != nil {
			t.Fatal(err)
		}
		form := func(pr, y string) url.Values { return url.Values{"t": {tok[1]}, "pr": {pr}, "rule": {y}} }
		for _, c := range []struct {
			name string
			f    url.Values
			code int
		}{
			{"bad token", url.Values{"t": {"x"}, "pr": {"acme/app#3"}, "rule": {string(rule)}}, http.StatusForbidden},
			{"bad PR", form("acme/app", string(rule)), http.StatusBadRequest},
			{"invalid rule", form("acme/app#3", "id: x\n"), http.StatusBadRequest},
			{"unreadable repo", form("https://github.com/acme/secret/pull/3", string(rule)), http.StatusNotFound},
		} {
			if code, _ := post("/ui/playground", c.f); code != c.code {
				t.Errorf("%s = %d, want %d", c.name, code, c.code)
			}
		}
		code, loc := post("/ui/playground", form("https://github.com/acme/app/pull/3", string(rule)))
		if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/ui/playground/") {
			t.Fatalf("run = %d %s", code, loc)
		}
		_, body, _ = get(loc)
		has(t, body, []string{"no-raw-sql-concat", "acme/app#3", "Running"}, []string{"Save as a draft config PR"})
		id, _ := strconv.ParseInt(strings.TrimPrefix(loc, "/ui/playground/"), 10, 64)
		res := `{"ran": true, "findings": [{"path": "db/q.go", "start": 7, "end": 7, "message": "Concatenated <SQL>", "kept": true},
			{"path": "db/r.go", "start": 2, "end": 2, "message": "m", "kept": false, "reason": "refuted"}]}`
		if err := st.FinishPlaygroundRun(ctx, queries.FinishPlaygroundRunParams{ID: id, Status: "done", Result: []byte(res), CostUsd: 0.03}); err != nil {
			t.Fatal(err)
		}
		_, body, _ = get(loc)
		has(t, body, []string{"Findings (1 would post)", "db/q.go:7", "Concatenated &lt;SQL&gt;", "no (refuted)", "Save as a draft config PR", "$0.03"}, []string{"<SQL>"})
		save := tokenRE.FindStringSubmatch(body)
		if save == nil || save[1] != "propose_rule" {
			t.Fatalf("no save form: %v", save)
		}
		if code, _ := post("/ui/actions", url.Values{"action": {save[1]}, "target": {save[2]}, "t": {save[3]}}); code != http.StatusSeeOther ||
			!strings.HasPrefix(acted[len(acted)-1], "propose_rule "+save[2]+" alice") {
			t.Errorf("save = %d %v", code, acted)
		}
		// The budget: two runs a day here.
		if code, _ := post("/ui/playground", form("acme/app#3", string(rule))); code != http.StatusSeeOther {
			t.Errorf("second run = %d", code)
		}
		if code, _ := post("/ui/playground", form("acme/app#3", string(rule))); code != http.StatusTooManyRequests {
			t.Errorf("over budget = %d", code)
		}
		// Someone else's run is not found.
		other, _ := st.CreatePlaygroundRun(ctx, queries.CreatePlaygroundRunParams{Actor: "bob", Repo: "acme/app", PrNumber: 3, RuleID: "x", RuleYaml: "x"})
		if code, _, _ := get("/ui/playground/" + itoa(other)); code != http.StatusNotFound {
			t.Errorf("bob's run = %d", code)
		}
	})
	t.Run("signed out", func(t *testing.T) {
		signedIn = false
		defer func() { signedIn = true }()
		code, _, hd := get("/ui/rules/no-raw-sql?x=1")
		if code != http.StatusFound || hd.Get("Location") != "/login?return=%2Fui%2Frules%2Fno-raw-sql%3Fx%3D1" {
			t.Errorf("signed out = %d %s", code, hd.Get("Location"))
		}
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestRuleRowEFPBar(t *testing.T) {
	row := queries.UIRulesRow{Status: "active", NegW: 15, PosW: 85} // eFP 0.15
	for _, tc := range []struct {
		bar  float64
		want string
	}{{0.10, "over the eFP bar"}, {0.20, ""}} {
		if got := toRuleRow(row, tc.bar).Health; got != tc.want {
			t.Errorf("bar %.2f: health %q, want %q", tc.bar, got, tc.want)
		}
	}
}

func TestEFPBarFromCurrentConfig(t *testing.T) {
	st := storetest.New(t)
	h := &Handler{cfg: Config{Store: st, Log: slog.New(slog.DiscardHandler)}}
	if got := h.efpBar(t.Context()); got != demote.DefaultEFPBar {
		t.Fatalf("no stored config: bar %v, want the default %v", got, demote.DefaultEFPBar)
	}
	tree, err := config.ReadTree(os.DirFS("../../examples/exemplar-config"))
	if err != nil {
		t.Fatal(err)
	}
	put := func(version string, bar float64) {
		t.Helper()
		g := *tree["global.yaml"]
		if bar > 0 {
			g.Data = append(slices.Clone(g.Data), fmt.Sprintf("\nfeedback:\n  efp_bar: %v\n  role_weights: {author: 1, reviewer: 2, owner: 4}\n"+
				"  reaction_weight: 0.5\n  demotion_wilson_floor: 0.8\n  demotion_requires: owner_adjudication_or_two_non_author_actors\n  owner_response_days: 14\n", bar)...)
		}
		files := maps.Clone(tree)
		files["global.yaml"] = &g
		enc, err := config.EncodeTree(files)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.PutConfigSnapshot(t.Context(), queries.PutConfigSnapshotParams{Version: version, Files: enc}); err != nil {
			t.Fatal(err)
		}
	}
	put("v1", 0.25)
	if got := h.efpBar(t.Context()); got != 0.25 {
		t.Fatalf("stored efp_bar 0.25: bar %v", got)
	}
	put("v2", 0) // a later version without a feedback block
	if got := h.efpBar(t.Context()); got != demote.DefaultEFPBar {
		t.Fatalf("current config without feedback: bar %v, want the default", got)
	}
}
