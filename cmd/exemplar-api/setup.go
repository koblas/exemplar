package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/koblas/exemplar/internal/envcfg"
	"github.com/koblas/exemplar/internal/setup"
	"github.com/koblas/exemplar/internal/telemetry"
)

type setupFlags struct {
	addr, setupAddr, setupURL, namespace string
	drain                                time.Duration
}

// setupPending is what the public listener says in setup mode.
const setupPending = `Exemplar is waiting for setup. From a machine with access to the cluster:

  kubectl -n %s port-forward deployment/exemplar-api 8081

then open http://localhost:8081/setup
`

// runSetup serves setup mode until SIGTERM: probes and a pointer on the
// public listener, and the setup page on the loopback one. It needs no
// database or keys, so a first install can start here.
func runSetup(f setupFlags) error {
	urls, err := envcfg.PublicURLs(os.Getenv)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log, shutdown, err := telemetry.Setup(ctx, "exemplar-api")
	if err != nil {
		return fmt.Errorf("set up telemetry: %w", err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	h, err := setup.New(setup.Config{
		URLs: urls, ConfigRepo: os.Getenv("EXEMPLAR_CONFIG_REPO"),
		GitHubWeb: os.Getenv("EXEMPLAR_GH_WEB_URL"), GitHubAPI: os.Getenv("EXEMPLAR_GH_API_URL"),
		Base: f.setupURL, Namespace: f.namespace, Log: log,
		HTTP: &http.Client{Timeout: 30 * time.Second},
	})
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}

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
	public.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(w, setupPending, f.namespace)
	})
	servers := []*http.Server{newServer(f.addr, public), newServer(f.setupAddr, h.Routes())}
	errc := make(chan error, len(servers))
	for _, hs := range servers {
		go func() {
			if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	log.Info("exemplar-api is in setup mode: no EXEMPLAR_GH_APP_ID",
		"addr", f.addr, "setup_addr", f.setupAddr,
		"next", fmt.Sprintf("kubectl -n %s port-forward deployment/exemplar-api 8081, then open %s/setup", f.namespace, f.setupURL))
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	draining.Store(true)
	time.Sleep(f.drain)
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var errs []error
	for _, hs := range servers {
		if err := hs.Shutdown(sctx); err != nil {
			errs = append(errs, fmt.Errorf("shut down %s: %w", hs.Addr, err))
		}
	}
	return errors.Join(errs...)
}

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: http.TimeoutHandler(h, 40*time.Second, "timeout"),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 45 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
	}
}
