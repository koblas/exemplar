// Command exemplar-gateway is the only process that holds model provider
// keys. Callers send Anthropic Messages requests to POST /v1/messages with
// the X-Exemplar-* headers; the gateway re-scans for secrets, routes the
// stage through its ordered provider list with failover, and records every
// call in model_calls.
//
// Configuration (environment):
//
//	EXEMPLAR_CONFIG_DIR        exemplar-config checkout (reads global.yaml)
//	EXEMPLAR_GATEWAY_TOKEN     shared secret callers send as x-api-key
//	EXEMPLAR_DATABASE_URL      optional; enables model_calls recording and shared
//	                           batch records (required with more than one replica)
//
// Providers are built for whichever of them global.yaml routes to:
//
//	claude   ANTHROPIC_API_KEY (from the KMS-backed secret); EXEMPLAR_CLAUDE_BASE_URL optional
//	bedrock  EXEMPLAR_BEDROCK_REGION (or AWS_REGION); AWS credentials from the
//	         default chain (workload identity, instance role, AWS_* env) or a
//	         Bedrock API key in AWS_BEARER_TOKEN_BEDROCK; EXEMPLAR_BEDROCK_BASE_URL
//	         and EXEMPLAR_BEDROCK_PROFILE optional
//	EXEMPLAR_GATEWAY_ADDR      listen address (default :8080)
//	EXEMPLAR_GATEWAY_RPM       requests per minute per model and per caller (default 600)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

// shutdownTimeout must stay below the pod's termination grace period.
const shutdownTimeout = 25 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("exemplar-gateway exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	log, shutdownTracing, err := telemetry.Setup(ctx, "exemplar-gateway")
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

	cfg, st, err := loadConfig(ctx)
	if err != nil {
		return err
	}
	if st != nil {
		defer st.Close()
	}
	gw, err := llm.NewGateway(cfg)
	if err != nil {
		return err
	}

	var ready atomic.Bool
	mux := http.NewServeMux()
	api := otelhttp.NewHandler(gw.Handler(), "messages")
	mux.Handle("/v1/messages", api)
	mux.Handle("/v1/messages/", api)
	// Liveness never depends on providers or the database.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	addr := envOr("EXEMPLAR_GATEWAY_ADDR", ":8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      11 * time.Minute, // provider calls can run long
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()
	ready.Store(true)

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	ready.Store(false)
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(sctx)
}

func loadConfig(ctx context.Context) (llm.Config, *store.Store, error) {
	var cfg llm.Config
	dir := os.Getenv("EXEMPLAR_CONFIG_DIR")
	if dir == "" {
		return cfg, nil, errors.New("EXEMPLAR_CONFIG_DIR is required")
	}
	data, err := os.ReadFile(filepath.Join(dir, "global.yaml"))
	if err != nil {
		return cfg, nil, fmt.Errorf("read global.yaml: %w", err)
	}
	var global schema.GlobalConfig
	if err := schema.Decode(schema.DocGlobal, data, &global); err != nil {
		return cfg, nil, err
	}
	token := os.Getenv("EXEMPLAR_GATEWAY_TOKEN")
	if token == "" {
		return cfg, nil, errors.New("EXEMPLAR_GATEWAY_TOKEN is required")
	}
	if err := llm.ValidateProviders(global.Providers); err != nil {
		return cfg, nil, err
	}
	// Build exactly the providers global.yaml routes to; a missing
	// credential for any of them stops start-up.
	clients := map[string]llm.Provider{}
	for _, name := range llm.ConfiguredProviders(global.Providers) {
		p, err := llm.NewProvider(ctx, name, llm.EnvSettings())
		if err != nil {
			return cfg, nil, err
		}
		clients[name] = p
	}
	scanner, err := redact.New()
	if err != nil {
		return cfg, nil, fmt.Errorf("secret scanner: %w", err)
	}
	rpm, _ := strconv.Atoi(os.Getenv("EXEMPLAR_GATEWAY_RPM"))
	cfg = llm.Config{
		Providers:         global.Providers,
		Clients:           clients,
		Scanner:           scanner,
		Token:             token,
		SoftCeilingUSD:    global.Budgets.SoftCeilingUSD,
		RequestsPerMinute: rpm,
	}

	var st *store.Store
	if url := os.Getenv("EXEMPLAR_DATABASE_URL"); url != "" {
		st, err = store.Open(ctx, url)
		if err != nil {
			return cfg, nil, err
		}
		cfg.Recorder = store.CallRecorder{Store: st}
		cfg.Batches = store.BatchStore{Store: st}
		cfg.SpendLoader = func(ctx context.Context, reviewID int64) (float64, error) {
			return st.ReviewSpend(ctx, &reviewID)
		}
	}
	return cfg, st, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
