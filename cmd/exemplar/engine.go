package main

import (
	"errors"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/redact"
)

// modelFlags choose how agents reach a model.
type modelFlags struct {
	gatewayURL   string
	gatewayToken string
	scripted     bool
}

func (f *modelFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.gatewayURL, "gateway-url", os.Getenv("EXEMPLAR_GATEWAY_URL"), "exemplar-gateway base URL (EXEMPLAR_GATEWAY_URL)")
	cmd.Flags().StringVar(&f.gatewayToken, "gateway-token", os.Getenv("EXEMPLAR_GATEWAY_TOKEN"), "gateway shared secret (EXEMPLAR_GATEWAY_TOKEN)")
	cmd.Flags().BoolVar(&f.scripted, "scripted", false, "use a scripted model that opens every file and reports nothing (offline dry run)")
}

// harness returns the live harness, a scripted one, or nil when neither
// is configured and allowNone is set.
func (f *modelFlags) harness(allowNone bool) (*engine.Harness, error) {
	switch {
	case f.scripted:
		tr := &engine.ScriptedTransport{Script: engine.Script{
			Generator: func(_ string, files []string) ([]string, string) { return files, `{"violations": []}` },
		}}
		return engine.NewHarness(engine.HarnessConfig{GatewayURL: "http://scripted.invalid", GatewayToken: "-", HTTPClient: &http.Client{Transport: tr}}), nil
	case f.gatewayURL != "" && f.gatewayToken != "":
		return engine.NewHarness(engine.HarnessConfig{GatewayURL: f.gatewayURL, GatewayToken: f.gatewayToken}), nil
	case allowNone:
		return nil, nil
	}
	return nil, errors.New("set --gateway-url and --gateway-token, or --scripted")
}

func newScanner() (*redact.Scanner, error) { return redact.New() }

func compiledRunner() compiled.Runner {
	return compiled.Runner{ASTGrep: os.Getenv("EXEMPLAR_AST_GREP")}
}
