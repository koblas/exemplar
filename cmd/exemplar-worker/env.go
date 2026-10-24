package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/envcfg"
	"github.com/koblas/exemplar/internal/feedbacklink"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/pathhistory"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/search/api"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/worker"
)

// Launchers for serve --launcher.
const (
	launcherInProcess  = "in-process"
	launcherKubernetes = "kubernetes"
)

// workerEnv is the settings serve and run-job share.
type workerEnv struct {
	dbURL, gwURL, gwToken string
	keys                  *envcfg.Keys
	app                   *gh.App
}

func envFromOS() (*workerEnv, error) {
	e := &workerEnv{dbURL: os.Getenv("EXEMPLAR_DATABASE_URL"), gwURL: os.Getenv("EXEMPLAR_GATEWAY_URL")}
	if e.dbURL == "" || e.gwURL == "" {
		return nil, fmt.Errorf("EXEMPLAR_DATABASE_URL and EXEMPLAR_GATEWAY_URL are %w", errRequired)
	}
	e.keys = envcfg.NewKeys(os.Getenv, os.ReadFile)
	var err error
	if e.gwToken, err = e.keys.Token("EXEMPLAR_GATEWAY_TOKEN", envcfg.LabelGatewayToken); err != nil {
		return nil, fmt.Errorf("gateway token: %w", err)
	}
	if e.app, err = appFromEnv(); err != nil {
		return nil, err
	}
	return e, nil
}

// workerConfig wires the review engine and everything a review needs but
// the config snapshot, which the caller sets.
func (e *workerEnv) workerConfig(st *store.Store, workDir string, log *slog.Logger) (worker.Config, error) {
	sc, err := redact.New()
	if err != nil {
		return worker.Config{}, fmt.Errorf("create redaction scanner: %w", err)
	}
	eng := engine.New(engine.NewHarness(engine.HarnessConfig{GatewayURL: e.gwURL, GatewayToken: e.gwToken}), sc,
		compiled.Runner{ASTGrep: os.Getenv("EXEMPLAR_AST_GREP")})
	if url := os.Getenv("EXEMPLAR_SEARCH_URL"); url != "" {
		tok, err := e.keys.Token("EXEMPLAR_SEARCH_TOKEN", envcfg.LabelSearchToken)
		if err != nil {
			return worker.Config{}, fmt.Errorf("search token: %w", err)
		}
		search, err := api.NewClient(url, tok, nil)
		if err != nil {
			return worker.Config{}, fmt.Errorf("create search client: %w", err)
		}
		hist, err := pathhistory.New(st, pathhistory.Remote(search))
		if err != nil {
			return worker.Config{}, fmt.Errorf("create path history: %w", err)
		}
		eng.WithReferences(search).WithPathHistory(hist)
	} else {
		log.Warn("EXEMPLAR_SEARCH_URL not set: agents get no find_references or path_history, and bug.contract is skipped")
	}

	// Mode eval and the playground run rule bodies nobody merged: cap
	// each engine run so one cannot take the pod's memory.
	evalMatcher := compiled.Runner{ASTGrep: os.Getenv("EXEMPLAR_AST_GREP")}
	if lim, ok := compiled.LimitCommand(compiled.EvalMemoryLimit, compiled.EvalCPULimit); ok {
		evalMatcher.Sandbox = lim
	} else {
		log.Warn("prlimit unavailable: mode eval and the playground run compiled rules without resource limits")
	}
	wcfg := worker.Config{
		GitHub: worker.AppClients{App: e.app}, Store: st,
		Engine: eng, Scanner: sc, WorkDir: workDir, Log: log,
		// Mode eval (config CI) evaluates exemplar-config's changed rules.
		CloneURL:   gh.CloneURL(os.Getenv("EXEMPLAR_GH_WEB_URL")),
		ConfigRepo: os.Getenv("EXEMPLAR_CONFIG_REPO"), Matcher: evalMatcher,
	}
	// Feedback links need exemplar-api's URL and the key it
	// verifies with; without them comments name the slash commands only.
	if apiURL := os.Getenv("EXEMPLAR_API_URL"); apiURL != "" {
		key, err := e.keys.File("EXEMPLAR_LINK_KEY_FILE", envcfg.LabelLink)
		if err != nil {
			return worker.Config{}, fmt.Errorf("feedback links: %w", err)
		}
		if wcfg.Links, err = feedbacklink.New(key, apiURL); err != nil {
			return worker.Config{}, fmt.Errorf("create feedback link signer: %w", err)
		}
	}
	return wcfg, nil
}
