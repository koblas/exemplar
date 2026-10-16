// Command exemplar-webhook receives the GitHub App's webhooks.
//
//	exemplar-webhook [--addr :8080] [--path /webhook]
//
// It verifies X-Hub-Signature-256 against the App's webhook secret, stores
// each delivery in gh_events (X-GitHub-Delivery is the idempotency key),
// and enqueues a gh_event job in the same transaction; exemplar-worker
// routes the jobs. It answers 202 within GitHub's 10 s window and holds no
// state, so it runs as 2+ replicas.
//
//	EXEMPLAR_DATABASE_URL           Postgres
//	EXEMPLAR_WEBHOOK_SECRET_FILE    file holding the App's webhook secret
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
	"github.com/koblas/exemplar/internal/webhook"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("exemplar-webhook", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "listen address")
	path := fs.String("path", "/webhook", "webhook path")
	drain := fs.Duration("drain", 5*time.Second, "how long readiness reports false before stopping")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dbURL, secretFile := os.Getenv("EXEMPLAR_DATABASE_URL"), os.Getenv("EXEMPLAR_WEBHOOK_SECRET_FILE")
	if dbURL == "" || secretFile == "" {
		return errors.New("EXEMPLAR_DATABASE_URL and EXEMPLAR_WEBHOOK_SECRET_FILE are required")
	}
	secret, err := os.ReadFile(secretFile) //nolint:gosec // G703: the path comes from the operator's environment, not from a request
	if err != nil {
		return fmt.Errorf("read webhook secret: %w", err)
	}
	secret = bytes.TrimSpace(secret)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log, shutdown, err := telemetry.Setup(ctx, "exemplar-webhook")
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()

	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	// An insert-only River client: no queues, no workers.
	client, err := river.NewClient[pgx.Tx](riverpgxv5.New(st.Pool), &river.Config{Logger: log})
	if err != nil {
		return err
	}
	h, err := webhook.New(secret, st, webhook.RiverEnqueuer{Client: client}, log)
	if err != nil {
		return err
	}

	var draining atomic.Bool
	mux := http.NewServeMux()
	mux.Handle("POST "+*path, h)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if draining.Load() {
			http.Error(w, "draining", http.StatusServiceUnavailable)
			return
		}
		pctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := st.Pool.Ping(pctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	// GitHub gives a delivery 10 s; stay well inside it.
	hs := &http.Server{
		Addr: *addr, Handler: http.TimeoutHandler(mux, 8*time.Second, "timeout"),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 8 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("exemplar-webhook serving", "addr", *addr, "path", *path)
		if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	draining.Store(true)
	time.Sleep(*drain)
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hs.Shutdown(sctx)
}
