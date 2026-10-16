package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/queries"
)

// The playground: paste a draft rule, or start from an
// existing one, pick a PR the viewer can read, and run the rule alone on
// it in exemplar-worker (nothing is posted on the PR). A finished run can
// be saved as a draft config PR (owner action propose_rule).

// maxRuleYAML bounds a draft rule.
const maxRuleYAML = 64 << 10

var prRefRE = regexp.MustCompile(`^(?:https://[^/]+/)?([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)(?:#|/pull/)([0-9]{1,9})/?$`)

// parsePR reads org/repo#N or a PR URL.
func parsePR(s string) (string, int, bool) {
	m := prRefRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", 0, false
	}
	n, _ := strconv.Atoi(m[2])
	return m[1], n, n > 0
}

type playgroundBody struct {
	PR, YAML, Error string
	Token           string
	Runs            []queries.PlaygroundRunsByRow
	Remaining       int
}

func (h *Handler) playground(_ http.ResponseWriter, r *http.Request, v *viewer) (string, string, any, error) {
	b := playgroundBody{Token: h.token(v.Login, "playground", "new"), PR: r.URL.Query().Get("pr")}
	if id := r.URL.Query().Get("rule"); ruleIDRE.MatchString(id) {
		// Start from an existing rule the viewer may see.
		spec, err := h.cfg.Store.UIRuleSpec(r.Context(), id)
		if err == nil && visibleRule(v, spec.Scope) {
			if y, err := yaml.JSONToYAML(spec.Spec); err == nil {
				b.YAML = string(y)
			}
		}
	}
	if from, err := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64); err == nil {
		// Again, from one of the viewer's runs.
		if run, err := h.cfg.Store.GetPlaygroundRun(r.Context(), from); err == nil && strings.EqualFold(run.Actor, v.Login) {
			b.YAML, b.PR = run.RuleYaml, fmt.Sprintf("%s#%d", run.Repo, run.PrNumber)
		}
	}
	var err error
	if b.Runs, err = h.cfg.Store.PlaygroundRunsBy(r.Context(), v.Login); err != nil {
		return "", "", nil, err
	}
	return "playground", "Playground", b, nil
}

func (h *Handler) tryRule(w http.ResponseWriter, r *http.Request) {
	login, read, ok := h.cfg.Session(r)
	if !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRuleYAML+maxForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form is too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !h.validToken(login, "playground", "new", r.PostForm.Get("t")) {
		http.Error(w, "this form did not come from this page; reload it", http.StatusForbidden)
		return
	}
	b := playgroundBody{Token: r.PostForm.Get("t"), PR: r.PostForm.Get("pr"), YAML: r.PostForm.Get("rule")}
	ctx := r.Context()
	fail := func(code int, msg string) {
		b.Error = msg
		b.Runs, _ = h.cfg.Store.PlaygroundRunsBy(ctx, login)
		h.render(w, code, "playground", pageData{Title: "Playground", Nav: "playground", Login: login, Body: b})
	}
	repo, pr, ok := parsePR(b.PR)
	if !ok {
		fail(http.StatusBadRequest, "Name the PR as org/repo#123 or its URL.")
		return
	}
	var rule schema.Rule
	if err := schema.Decode(schema.DocRule, []byte(b.YAML), &rule); err != nil {
		fail(http.StatusBadRequest, "The rule is not valid: "+err.Error())
		return
	}
	if ok, err := read(r.Context(), repo); err != nil || !ok {
		fail(http.StatusNotFound, "No such repo, or you cannot read it.")
		return
	}
	day := time.Now().UTC().Truncate(24 * time.Hour)
	spent, err := h.cfg.Store.PlaygroundSpend(r.Context(), queries.PlaygroundSpendParams{Actor: login, Since: pgtype.Timestamptz{Time: day, Valid: true}})
	if err != nil {
		fail(http.StatusInternalServerError, "Could not read your budget; try again.")
		return
	}
	if spent.Spent >= h.cfg.PlaygroundDailyUSD || int(spent.Runs) >= h.cfg.PlaygroundDailyRuns {
		fail(http.StatusTooManyRequests, fmt.Sprintf("Your daily playground budget is used (%d runs, $%.2f); it resets at 00:00 UTC.", spent.Runs, spent.Spent))
		return
	}
	id, err := h.cfg.Try(r.Context(), login, repo, pr, rule.ID, b.YAML)
	if err != nil {
		h.cfg.Log.Error("web: playground run not queued", "err", err)
		fail(http.StatusServiceUnavailable, "Could not queue the run; try again.")
		return
	}
	http.Redirect(w, r, "/ui/playground/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

type runBody struct {
	Run       queries.PlaygroundRun
	PR        string
	Result    *evaluate.TryResult
	Kept      int
	SaveToken string
}

func (h *Handler) playgroundRun(_ http.ResponseWriter, r *http.Request, v *viewer) (string, string, any, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return "", "", nil, errNotFound
	}
	run, err := h.cfg.Store.GetPlaygroundRun(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !strings.EqualFold(run.Actor, v.Login)) {
		return "", "", nil, errNotFound // someone else's run is as absent as none
	}
	if err != nil {
		return "", "", nil, err
	}
	b := runBody{Run: run, PR: h.prURL(run.Repo, run.PrNumber), SaveToken: h.token(v.Login, "propose_rule", strconv.FormatInt(id, 10))}
	if len(run.Result) > 0 {
		b.Result = &evaluate.TryResult{}
		if err := json.Unmarshal(run.Result, b.Result); err != nil {
			return "", "", nil, err
		}
		for _, f := range b.Result.Findings {
			if f.Kept {
				b.Kept++
			}
		}
	}
	return "playgroundrun", fmt.Sprintf("Playground run %d", id), b, nil
}
