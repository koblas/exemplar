// Package web is the web UI, served by exemplar-api
// under /ui/: rules and their health, rule and finding detail, the
// candidate queue, the auto-approve audit and the cost explorer.
//
// Every page needs a GitHub sign-in (org SSO sits in front at the
// ingress). Anything tied to a repo (findings, PRs, decisions, the
// comments a candidate was mined from, repo-scoped rules) is shown only
// for repos the viewer can read: their GitHub permission (checked with
// their user token, cached for 5 minutes) when they signed
// in with GitHub, or the repos their groups map to when they signed in
// with OIDC. Aggregates without repos (rule
// health, spend) are shown to every signed-in viewer.
//
// Pages are server-rendered with html/template and need no JavaScript.
// templ and htmx were considered; html/template needs neither a code
// generator nor a new dependency.
//
// Owner actions (shadow or kill a rule, kill a candidate, turn a repo's
// auto-approve off) are forms: a POST records the action with the
// signed-in actor and enqueues an owner_action job, which exemplar-worker
// runs with the config-writer token after checking the actor may (see
// internal/owneract). Marking a finding a false positive enqueues the
// same feedback job a feedback link does. Every form carries a token
// bound to the viewer, the action and its target.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/learn/demote"
	"github.com/koblas/exemplar/internal/setup"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Store reads what the pages show (store.Store).
type Store interface { //nolint:interfacebloat // exactly the store reads the pages use; one fake implements it
	UIRules(ctx context.Context, days int32) ([]queries.UIRulesRow, error)
	UIRuleWeeks(ctx context.Context, ruleID string) ([]queries.UIRuleWeeksRow, error)
	UIRuleFindings(ctx context.Context, arg queries.UIRuleFindingsParams) ([]queries.UIRuleFindingsRow, error)
	UIFinding(ctx context.Context, id int64) (queries.UIFindingRow, error)
	UIFindingEvents(ctx context.Context, findingID int64) ([]queries.UIFindingEventsRow, error)
	UICandidates(ctx context.Context, limit int32) ([]queries.UICandidatesRow, error)
	UIDecisions(ctx context.Context, limit int32) ([]queries.UIDecisionsRow, error)
	UICost(ctx context.Context, days int32) ([]queries.UICostRow, error)
	OwnerActionsBy(ctx context.Context, actor string) ([]queries.OwnerAction, error)
	GetPlaygroundRun(ctx context.Context, id int64) (queries.PlaygroundRun, error)
	PlaygroundRunsBy(ctx context.Context, actor string) ([]queries.PlaygroundRunsByRow, error)
	PlaygroundSpend(ctx context.Context, arg queries.PlaygroundSpendParams) (queries.PlaygroundSpendRow, error)
	UIRuleSpec(ctx context.Context, ruleID string) (queries.UIRuleSpecRow, error)
	CurrentConfigSnapshot(ctx context.Context) (queries.CurrentConfigSnapshotRow, error)
}

// Access reports whether the viewer can read a repo: their GitHub
// permission, or the repos their OIDC groups map to (api.UISession).
type Access func(ctx context.Context, repo string) (bool, error)

// Config wires the UI.
type Config struct {
	Store Store
	// Session returns the signed-in viewer and their repo access.
	Session func(r *http.Request) (login string, access Access, ok bool)
	// SignOut clears the viewer's session.
	SignOut func(w http.ResponseWriter)
	// GitHubWeb is https://github.com/ (GHES: https://<host>/).
	GitHubWeb string
	// Act records an owner action and enqueues its job, returning its id.
	Act func(ctx context.Context, action, target, actor, reason string) (int64, error)
	// FalsePositive enqueues a viewer's "wrong" feedback on a finding.
	FalsePositive func(ctx context.Context, findingID int64, actor, text string) error
	// Try records a playground run and enqueues its job, returning its id.
	Try func(ctx context.Context, actor, repo string, pr int, ruleID, ruleYAML string) (int64, error)
	// PlaygroundDailyUSD and PlaygroundDailyRuns bound one viewer's
	// playground per UTC day (defaults $5 and 20).
	PlaygroundDailyUSD  float64
	PlaygroundDailyRuns int
	// FormKey signs the forms' tokens; at least 32 bytes.
	FormKey []byte
	// Checks runs the installation checks /ui/setup shows (internal/setup);
	// nil shows none.
	Checks func(ctx context.Context) []setup.Check
	Log    *slog.Logger
}

// Window is the stats window, in days.
const Window = 28

// Handler serves the UI.
type Handler struct{ cfg Config }

// New validates cfg.
func New(cfg Config) (*Handler, error) {
	if cfg.Store == nil || cfg.Session == nil || cfg.SignOut == nil || cfg.Act == nil || cfg.FalsePositive == nil || cfg.Try == nil {
		return nil, errMissingDependency
	}
	if cfg.PlaygroundDailyUSD <= 0 {
		cfg.PlaygroundDailyUSD = 5
	}
	if cfg.PlaygroundDailyRuns <= 0 {
		cfg.PlaygroundDailyRuns = 20
	}
	if len(cfg.FormKey) < 32 {
		return nil, errShortFormKey
	}
	if cfg.GitHubWeb == "" {
		cfg.GitHubWeb = "https://github.com/"
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Handler{cfg: cfg}, nil
}

// Routes returns the /ui/ routes.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui/{$}", h.page(h.rules))
	mux.HandleFunc("GET /ui/rules/{id}", h.page(h.rule))
	mux.HandleFunc("GET /ui/findings/{id}", h.page(h.finding))
	mux.HandleFunc("GET /ui/candidates", h.page(h.candidates))
	mux.HandleFunc("GET /ui/audit", h.page(h.audit))
	mux.HandleFunc("GET /ui/cost", h.page(h.cost))
	mux.HandleFunc("GET /ui/actions", h.page(h.actions))
	mux.HandleFunc("GET /ui/setup", h.page(h.setupChecks))
	mux.HandleFunc("GET /ui/setup.json", h.setupJSON)
	mux.HandleFunc("GET /ui/playground", h.page(h.playground))
	mux.HandleFunc("POST /ui/playground", h.tryRule)
	mux.HandleFunc("GET /ui/playground/{id}", h.page(h.playgroundRun))
	mux.HandleFunc("POST /ui/actions", h.act)
	mux.HandleFunc("POST /ui/findings/{id}/false-positive", h.falsePositive)
	mux.HandleFunc("POST /ui/signout", func(w http.ResponseWriter, _ *http.Request) {
		h.cfg.SignOut(w)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Signed out.\n"))
	})
	return secure(mux)
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// viewer is the signed-in person on one request, with their repo access
// memoized for the request on top of the 5-minute cache.
type viewer struct {
	h      *Handler
	ctx    context.Context //nolint:containedctx // request-scoped: a viewer lives for one request and v.can checks access with it
	Login  string
	read   Access
	access map[string]bool
	err    error
}

// can reports whether the viewer can read repo. A failed check denies,
// and is remembered so the page can say access could not be checked.
func (v *viewer) can(repo string) bool {
	if ok, seen := v.access[repo]; seen {
		return ok
	}
	ok, err := v.read(v.ctx, repo)
	if err != nil {
		v.h.cfg.Log.Warn("web: repo access not checked", "repo", repo, "err", err)
		v.err = err
		ok = false
	}
	v.access[repo] = ok
	return ok
}

// pageData is what every page renders with.
type pageData struct {
	Title  string
	Nav    string
	Login  string
	Body   any
	Notice string
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, v *viewer) (string, string, any, error)

var errNotFound = errors.New("not found")

var (
	errMissingDependency = errors.New("web: Store, Session, SignOut, Act, FalsePositive and Try are required")
	errShortFormKey      = errors.New("web: the form key must be at least 32 bytes")
)

func (h *Handler) page(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		login, read, ok := h.cfg.Session(r)
		if !ok {
			http.Redirect(w, r, "/login?return="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		v := &viewer{h: h, ctx: r.Context(), Login: login, read: read, access: map[string]bool{}}
		name, title, body, err := fn(w, r, v)
		switch {
		case errors.Is(err, errNotFound) || errors.Is(err, pgx.ErrNoRows):
			h.render(w, http.StatusNotFound, "notfound", pageData{Title: "Not found", Login: login})
			return
		case err != nil:
			h.cfg.Log.Error("web: page failed", "path", r.URL.Path, "err", err)
			h.render(w, http.StatusInternalServerError, "error", pageData{Title: "Something went wrong", Login: login})
			return
		}
		d := pageData{Title: title, Nav: name, Login: login, Body: body}
		if v.err != nil {
			d.Notice = "Some repos' access could not be checked with GitHub just now; their rows are hidden."
		}
		h.render(w, http.StatusOK, name, d)
	}
}

func (h *Handler) render(w http.ResponseWriter, code int, name string, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := pages.ExecuteTemplate(w, name, d); err != nil {
		h.cfg.Log.Warn("web: render", "page", name, "err", err)
	}
}

func (h *Handler) prURL(repo string, pr int32) string {
	return fmt.Sprintf("%s%s/pull/%d", h.cfg.GitHubWeb, repo, pr)
}

// ruleRow is one row of the rules page.
type ruleRow struct {
	queries.UIRulesRow

	Acceptance, EFP string
	Health          string
}

func pct(n, d float64) string {
	if d == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", 100*n/d)
}

// efpBar is feedback.efp_bar in the config version the worker loaded
// last, so the rules page flags what demotion acts on; the demotion
// default when no version is stored or it does not load.
func (h *Handler) efpBar(ctx context.Context) float64 {
	cur, err := h.cfg.Store.CurrentConfigSnapshot(ctx)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.cfg.Log.Warn("reading the current config failed; using the default eFP bar", "err", err)
		}
		return demote.DefaultEFPBar
	}
	tree, err := config.DecodeTree(cur.Files)
	if err != nil {
		h.cfg.Log.Warn("decoding the current config failed; using the default eFP bar", "version", cur.Version, "err", err)
		return demote.DefaultEFPBar
	}
	snap, err := config.LoadSnapshot(tree, cur.Version)
	if err != nil {
		h.cfg.Log.Warn("loading the current config failed; using the default eFP bar", "version", cur.Version, "err", err)
		return demote.DefaultEFPBar
	}
	if f := snap.Global.Feedback; f != nil && f.EFPBar > 0 {
		return f.EFPBar
	}
	return demote.DefaultEFPBar
}

func toRuleRow(r queries.UIRulesRow, efpBar float64) ruleRow {
	row := ruleRow{UIRulesRow: r, Acceptance: pct(float64(r.Accepted), float64(r.Judged)), EFP: pct(r.NegW, r.NegW+r.PosW)}
	switch {
	case r.Status != "active" && r.Status != "shadow":
		row.Health = ""
	case r.NegW+r.PosW == 0:
		row.Health = "no feedback yet"
	case r.NegW/(r.NegW+r.PosW) > efpBar:
		row.Health = "over the eFP bar"
	}
	return row
}

// visibleRule reports whether the viewer may see a rule: global rules
// always, a repo's own rules only with read access to it.
func visibleRule(v *viewer, scope string) bool {
	return scope == "global" || v.can(scope)
}

type rulesBody struct {
	Rules                 []ruleRow
	Owners, Statuses      []string
	Owner, Status, Search string
	Window                int
}

func (h *Handler) rules(_ http.ResponseWriter, r *http.Request, v *viewer) (string, string, any, error) {
	rows, err := h.cfg.Store.UIRules(r.Context(), Window)
	if err != nil {
		return "", "", nil, fmt.Errorf("list rules: %w", err)
	}
	q := r.URL.Query()
	b := rulesBody{Owner: q.Get("owner"), Status: q.Get("status"), Search: strings.TrimSpace(q.Get("q")), Window: Window}
	owners, statuses := map[string]bool{}, map[string]bool{}
	bar := h.efpBar(r.Context())
	for _, row := range rows {
		if !visibleRule(v, row.Scope) {
			continue
		}
		owners[row.Owner], statuses[row.Status] = true, true
		if (b.Owner != "" && row.Owner != b.Owner) || (b.Status != "" && row.Status != b.Status) ||
			(b.Search != "" && !strings.Contains(strings.ToLower(row.RuleID+" "+row.Category), strings.ToLower(b.Search))) {
			continue
		}
		b.Rules = append(b.Rules, toRuleRow(row, bar))
	}
	for o := range owners {
		b.Owners = append(b.Owners, o)
	}
	for s := range statuses {
		b.Statuses = append(b.Statuses, s)
	}
	slices.Sort(b.Owners)
	slices.Sort(b.Statuses)
	return "rules", "Rules", b, nil
}

type findingRow struct {
	queries.UIRuleFindingsRow

	PR, Outcome string
}

type ruleBody struct {
	Rule                   ruleRow
	Weeks                  []queries.UIRuleWeeksRow
	Findings               []findingRow
	Hidden                 int
	ShadowToken, KillToken string
}

func (h *Handler) rule(_ http.ResponseWriter, r *http.Request, v *viewer) (string, string, any, error) {
	id := r.PathValue("id")
	rows, err := h.cfg.Store.UIRules(r.Context(), Window)
	if err != nil {
		return "", "", nil, fmt.Errorf("list rules: %w", err)
	}
	i := slices.IndexFunc(rows, func(x queries.UIRulesRow) bool { return x.RuleID == id })
	if i < 0 || !visibleRule(v, rows[i].Scope) {
		return "", "", nil, errNotFound
	}
	b := ruleBody{Rule: toRuleRow(rows[i], h.efpBar(r.Context())), ShadowToken: h.token(v.Login, "shadow_rule", id), KillToken: h.token(v.Login, "kill_rule", id)}
	if b.Weeks, err = h.cfg.Store.UIRuleWeeks(r.Context(), id); err != nil {
		return "", "", nil, fmt.Errorf("list rule weeks: %w", err)
	}
	fs, err := h.cfg.Store.UIRuleFindings(r.Context(), queries.UIRuleFindingsParams{RuleID: id, Limit: 100})
	if err != nil {
		return "", "", nil, fmt.Errorf("list rule findings: %w", err)
	}
	for _, f := range fs {
		if !v.can(f.Repo) {
			b.Hidden++
			continue
		}
		outcome := "open"
		switch {
		case f.Accepted:
			outcome = "accepted"
		case f.Rejected:
			outcome = "rejected"
		case f.Ignored:
			outcome = "ignored"
		}
		b.Findings = append(b.Findings, findingRow{UIRuleFindingsRow: f, PR: h.prURL(f.Repo, f.PrNumber), Outcome: outcome})
	}
	return "rule", id, b, nil
}

type findingBody struct {
	F        queries.UIFindingRow
	PR       string
	Comment  string
	Events   []queries.UIFindingEventsRow
	Token    string
	Recorded bool
}

func (h *Handler) finding(_ http.ResponseWriter, r *http.Request, v *viewer) (string, string, any, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return "", "", nil, errNotFound
	}
	f, err := h.cfg.Store.UIFinding(r.Context(), id)
	if err != nil {
		return "", "", nil, fmt.Errorf("get finding: %w", err)
	}
	// Someone who cannot read the repo sees the same page as for no
	// finding at all.
	if !v.can(f.Repo) {
		return "", "", nil, errNotFound
	}
	b := findingBody{
		F: f, PR: h.prURL(f.Repo, f.PrNumber), Token: h.token(v.Login, "false_positive", strconv.FormatInt(id, 10)),
		Recorded: r.URL.Query().Get("recorded") == "1",
	}
	if f.GhCommentID != 0 {
		b.Comment = fmt.Sprintf("%s#discussion_r%d", b.PR, f.GhCommentID)
	}
	if b.Events, err = h.cfg.Store.UIFindingEvents(r.Context(), id); err != nil {
		return "", "", nil, fmt.Errorf("list finding events: %w", err)
	}
	return "finding", fmt.Sprintf("Finding %d", id), b, nil
}

type candidateRow struct {
	queries.UICandidatesRow

	Visible []string
	// Hidden: the viewer cannot read every source repo, so the title and
	// description (drawn from the source comments) are not shown.
	Hidden bool
	Token  string
}

func (h *Handler) candidates(_ http.ResponseWriter, r *http.Request, v *viewer) (string, string, any, error) {
	rows, err := h.cfg.Store.UICandidates(r.Context(), 200)
	if err != nil {
		return "", "", nil, fmt.Errorf("list candidates: %w", err)
	}
	var out []candidateRow
	for _, c := range rows {
		token := h.token(v.Login, "kill_candidate", strconv.FormatInt(c.ID, 10))
		// An owner seed's text is its rule file in exemplar-config, not
		// drawn from any repo's comments.
		if c.SourceKind == "owner_seed" {
			out = append(out, candidateRow{UICandidatesRow: c, Token: token})
			continue
		}
		var vis []string
		for _, repo := range c.Repos {
			if v.can(repo) {
				vis = append(vis, repo)
			}
		}
		// A candidate is text drawn from its source comments: listed for
		// viewers who can read at least one of their repos, its text only
		// for those who can read all of them.
		if len(vis) > 0 {
			row := candidateRow{UICandidatesRow: c, Visible: vis, Hidden: len(vis) < len(c.Repos), Token: token}
			if row.Hidden {
				row.Title, row.Description = "", ""
			}
			out = append(out, row)
		}
	}
	return "candidates", "Candidates", out, nil
}

type decisionRow struct {
	queries.UIDecisionsRow

	PR string
}

type auditBody struct {
	Decisions []decisionRow
	Repos     []repoSwitch
}

type repoSwitch struct{ Repo, Token string }

func (h *Handler) audit(_ http.ResponseWriter, r *http.Request, v *viewer) (string, string, any, error) {
	rows, err := h.cfg.Store.UIDecisions(r.Context(), 500)
	if err != nil {
		return "", "", nil, fmt.Errorf("list decisions: %w", err)
	}
	var b auditBody
	seen := map[string]bool{}
	for _, d := range rows {
		if v.can(d.Repo) {
			b.Decisions = append(b.Decisions, decisionRow{UIDecisionsRow: d, PR: h.prURL(d.Repo, d.PrNumber)})
			if !seen[d.Repo] {
				seen[d.Repo] = true
				b.Repos = append(b.Repos, repoSwitch{Repo: d.Repo, Token: h.token(v.Login, "kill_switch", d.Repo)})
			}
		}
	}
	slices.SortFunc(b.Repos, func(a, c repoSwitch) int { return strings.Compare(a.Repo, c.Repo) })
	return "audit", "Auto-approve audit", b, nil
}

type costBody struct {
	Rows     []queries.UICostRow
	ByCaller []costTotal
	Total    float64
	Days     int
}

type costTotal struct {
	Key   string
	Calls int
	USD   float64
}

func (h *Handler) cost(_ http.ResponseWriter, r *http.Request, _ *viewer) (string, string, any, error) {
	days := 30
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d >= 1 && d <= 365 {
		days = d
	}
	rows, err := h.cfg.Store.UICost(r.Context(), int32(days))
	if err != nil {
		return "", "", nil, fmt.Errorf("get cost: %w", err)
	}
	b := costBody{Rows: rows, Days: days}
	totals := map[string]*costTotal{}
	for _, c := range rows {
		k := c.Caller
		if k == "" {
			k = "(unattributed)"
		}
		t, ok := totals[k]
		if !ok {
			t = &costTotal{Key: k}
			totals[k] = t
		}
		t.Calls += int(c.Calls)
		t.USD += c.CostUsd
		b.Total += c.CostUsd
	}
	for _, t := range totals {
		b.ByCaller = append(b.ByCaller, *t)
	}
	slices.SortFunc(b.ByCaller, func(a, b costTotal) int {
		switch {
		case a.USD > b.USD:
			return -1
		case a.USD < b.USD:
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
	return "cost", "Cost", b, nil
}

// token signs a form for the viewer, the action and its target.
func (h *Handler) token(login, action, target string) string {
	m := hmac.New(sha256.New, h.cfg.FormKey)
	for _, p := range []string{"ui-form", strings.ToLower(login), action, target} {
		m.Write([]byte(p))
		m.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (h *Handler) validToken(login, action, target, tok string) bool {
	return hmac.Equal([]byte(tok), []byte(h.token(login, action, target)))
}

var (
	ruleIDRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
	repoFullRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	actions    = map[string]*regexp.Regexp{
		"shadow_rule": ruleIDRE, "kill_rule": ruleIDRE, "kill_candidate": regexp.MustCompile(`^[1-9][0-9]{0,18}$`), "kill_switch": repoFullRE,
		"propose_rule": regexp.MustCompile(`^[1-9][0-9]{0,18}$`),
	}
)

// maxForm bounds an action form.
const maxForm = 8 << 10

// act records an owner action and enqueues it; the worker decides
// whether the actor may take it, and /ui/actions shows the outcome.
func (h *Handler) act(w http.ResponseWriter, r *http.Request) {
	login, read, ok := h.cfg.Session(r)
	if !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	action, target := r.PostForm.Get("action"), r.PostForm.Get("target")
	re, known := actions[action]
	if !known || !re.MatchString(target) || !h.validToken(login, action, target, r.PostForm.Get("t")) {
		http.Error(w, "this form did not come from this page; reload it", http.StatusForbidden)
		return
	}
	// A repo's kill switch is only for viewers who can read the repo.
	if action == "kill_switch" {
		if ok, err := read(r.Context(), target); err != nil || !ok {
			http.Error(w, "no such repo, or you cannot read it", http.StatusNotFound)
			return
		}
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if rs := []rune(reason); len(rs) > 1000 {
		reason = string(rs[:1000])
	}
	id, err := h.cfg.Act(r.Context(), action, target, login, reason)
	if err != nil {
		h.cfg.Log.Error("web: owner action not queued", "action", action, "err", err)
		http.Error(w, "could not queue the action; try again", http.StatusServiceUnavailable)
		return
	}
	h.cfg.Log.Info("web: owner action queued", "id", id, "action", action, "target", target, "actor", login)
	http.Redirect(w, r, "/ui/actions", http.StatusSeeOther)
}

func (h *Handler) falsePositive(w http.ResponseWriter, r *http.Request) {
	login, read, ok := h.cfg.Session(r)
	if !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	idText := r.PathValue("id")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || !h.validToken(login, "false_positive", idText, r.PostForm.Get("t")) {
		http.Error(w, "this form did not come from this page; reload it", http.StatusForbidden)
		return
	}
	f, err := h.cfg.Store.UIFinding(r.Context(), id)
	if err != nil {
		http.Error(w, "no such finding", http.StatusNotFound)
		return
	}
	if ok, err := read(r.Context(), f.Repo); err != nil || !ok {
		http.Error(w, "no such finding", http.StatusNotFound)
		return
	}
	text := strings.TrimSpace(r.PostForm.Get("text"))
	if rs := []rune(text); len(rs) > 1000 {
		text = string(rs[:1000])
	}
	if err := h.cfg.FalsePositive(r.Context(), id, login, text); err != nil {
		h.cfg.Log.Error("web: false positive not queued", "finding", id, "err", err)
		http.Error(w, "could not record it; try again", http.StatusServiceUnavailable)
		return
	}
	// idText parsed as an int64 above, so this is always a same-origin /ui/ path.
	http.Redirect(w, r, "/ui/findings/"+idText+"?recorded=1", http.StatusSeeOther) //nolint:gosec // G710: fixed local path, numeric id
}

// setupChecks shows what the installation still needs. Every signed-in
// viewer sees it: it names the org and the webhook URL, nothing secret.
func (h *Handler) setupChecks(_ http.ResponseWriter, r *http.Request, _ *viewer) (string, string, any, error) {
	return "setup", "Setup", h.runChecks(r.Context()), nil
}

// setupJSON is /ui/setup for scripts, behind the same sign-in.
func (h *Handler) setupJSON(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.cfg.Session(r); !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"checks": h.runChecks(r.Context())}); err != nil {
		h.cfg.Log.Warn("web: setup checks", "err", err)
	}
}

func (h *Handler) runChecks(ctx context.Context) []setup.Check {
	if h.cfg.Checks == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return h.cfg.Checks(ctx)
}

func (h *Handler) actions(_ http.ResponseWriter, r *http.Request, v *viewer) (string, string, any, error) {
	rows, err := h.cfg.Store.OwnerActionsBy(r.Context(), v.Login)
	if err != nil {
		return "", "", nil, fmt.Errorf("list owner actions: %w", err)
	}
	return "actions", "Your actions", rows, nil
}
