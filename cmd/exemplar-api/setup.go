package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/koblas/exemplar/internal/envcfg"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/setup"
	"github.com/koblas/exemplar/internal/store"
)

type setupFlags struct {
	addr, namespace string
	drain           time.Duration
}

// checkEvery is how often a pod in setup mode looks for setup completed
// on another pod.
const checkEvery = 10 * time.Second

// runSetup serves setup mode until setup is complete, when it returns what
// setup wrote, or until ctx ends (nil): probes and /setup on the public
// listener. It needs no database or keys, so a first install can start
// here; setup writes the ones that are missing.
func runSetup(ctx context.Context, log *slog.Logger, secrets setup.Secrets, f setupFlags) (*setup.Installed, error) {
	urls, err := envcfg.PublicURLs(os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("setup: %w", err)
	}
	h, err := setup.New(setup.Config{
		URLs: urls, ConfigRepo: os.Getenv("EXEMPLAR_CONFIG_REPO"),
		GitHubWeb: os.Getenv("EXEMPLAR_GH_WEB_URL"), GitHubAPI: os.Getenv("EXEMPLAR_GH_API_URL"),
		Namespace: f.namespace, Secrets: secrets, Log: log,
		HasDatabase: os.Getenv("EXEMPLAR_DATABASE_URL") != "",
		HTTP:        &http.Client{Timeout: 30 * time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("setup: %w", err)
	}
	// Writes the token, the root key and the in-cluster database's Secrets
	// now, so Postgres starts while the operator registers the App. A
	// failure here shows again on the setup page, with what to do.
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := h.Prepare(pctx); err != nil {
		log.Warn("setup: preparing the installation's Secrets failed; /setup retries", "err", err)
	}
	cancel()

	var draining atomic.Bool
	public := http.NewServeMux()
	public.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	public.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if draining.Load() {
			http.Error(w, "draining", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	public.Handle("/", h.Routes())
	hs := newServer(f.addr, public)
	errc := make(chan error, 1)
	go func() {
		if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	// The token itself never reaches a log: reading it takes the same
	// access to the namespace's Secrets as the command below.
	log.Info("exemplar-api is in setup mode: no EXEMPLAR_GH_APP_ID", "addr", f.addr,
		"next", fmt.Sprintf("open %s/setup and sign in with the token from: kubectl -n %s get secret exemplar-setup -o jsonpath='{.data.token}' | base64 -d",
			strings.TrimSuffix(urls.API, "/"), f.namespace))

	in, err := waitInstalled(ctx, h, log, errc)
	if err != nil {
		return nil, err
	}
	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer scancel()
	if in == nil { // stopping: drain as the normal server does
		draining.Store(true)
		time.Sleep(f.drain)
	}
	if err := hs.Shutdown(sctx); err != nil {
		return nil, fmt.Errorf("shut down setup: %w", err)
	}
	return in, nil
}

// waitInstalled returns what setup wrote once it's complete (seen on this
// pod, or in the Secrets every checkEvery), nil when ctx ends, or the
// server's error.
func waitInstalled(ctx context.Context, h *setup.Handler, log *slog.Logger, errc <-chan error) (*setup.Installed, error) {
	tick := time.NewTicker(checkEvery)
	defer tick.Stop()
	for {
		in, err := h.Installed(ctx)
		if err != nil && ctx.Err() == nil {
			log.Warn("setup: reading the installation's Secrets failed", "err", err)
		}
		if in != nil {
			return in, nil
		}
		select {
		case err := <-errc:
			return nil, err
		case <-ctx.Done():
			return nil, nil //nolint:nilnil // stopped before setup finished: not an error
		case <-h.Finished():
		case <-tick.C:
		}
	}
}

// namespaceFile is the pod's namespace in its service account projection
// (deploy/k8s/core/api.yaml).
const namespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// podNamespace is flag, or the pod's own namespace, or exemplar.
func podNamespace(flag string) string {
	if flag != "" {
		return flag
	}
	if b, err := os.ReadFile(namespaceFile); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b))
	}
	return "exemplar"
}

// kubeSecrets reaches the namespace's Secrets with the pod's projected
// service account token (deploy/k8s/core/api.yaml).
func kubeSecrets(namespace string) (setup.Secrets, error) {
	rc, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("setup: Kubernetes API: in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("setup: Kubernetes API: client: %w", err)
	}
	return setup.KubeSecrets{Client: client, Namespace: namespace}, nil
}

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: http.TimeoutHandler(h, 40*time.Second, "timeout"),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 45 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
	}
}

// installChecks builds /ui/setup's checks from the review app's settings.
func installChecks(st *store.Store, e env) (func(context.Context) []setup.Check, error) {
	appID, err := strconv.ParseInt(e.get("EXEMPLAR_GH_APP_ID"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("EXEMPLAR_GH_APP_ID: %w", err)
	}
	key, err := e.read(e.get("EXEMPLAR_GH_PRIVATE_KEY_FILE"))
	if err != nil {
		return nil, fmt.Errorf("read EXEMPLAR_GH_PRIVATE_KEY_FILE: %w", err)
	}
	repo := e.get("EXEMPLAR_CONFIG_REPO")
	app, err := gh.NewApp(gh.AppConfig{AppID: appID, PrivateKey: key, ConfigRepo: repo, BaseURL: e.get("EXEMPLAR_GH_API_URL")}, nil)
	if err != nil {
		return nil, fmt.Errorf("create github app: %w", err)
	}
	urls, err := envcfg.PublicURLs(e.get)
	if err != nil {
		return nil, fmt.Errorf("public URLs: %w", err)
	}
	c := setup.Checker{
		App: app, Org: envcfg.Owner(repo), ConfigRepo: repo, WebhookURL: urls.Webhook,
		LastEvent: func(ctx context.Context) (time.Time, bool, error) {
			var at *time.Time
			if err := st.Pool.QueryRow(ctx, "SELECT max(received_at) FROM gh_events").Scan(&at); err != nil {
				return time.Time{}, false, fmt.Errorf("last event: %w", err)
			}
			if at == nil {
				return time.Time{}, false, nil
			}
			return *at, true, nil
		},
	}
	return c.Run, nil
}
