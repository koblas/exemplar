package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// sendMessages forwards a raw Messages request through an SDK message
// service and returns the raw response. Both built-in providers speak the
// Messages API, so the gateway's JSON passes through unchanged; the SDK
// supplies the endpoint, authentication and signing.
func sendMessages(ctx context.Context, svc anthropic.MessageService, req Request) (*Response, error) {
	var raw []byte
	opts := []option.RequestOption{
		option.WithRequestBody("application/json", bytes.NewReader(req.Body)),
		option.WithResponseBodyInto(&raw),
		option.WithMaxRetries(0), // failover is the gateway's job
	}
	if req.Beta != "" {
		opts = append(opts, option.WithHeader("anthropic-beta", req.Beta))
	}
	if _, err := svc.New(ctx, anthropic.MessageNewParams{}, opts...); err != nil {
		return nil, providerErr(ctx, err)
	}
	var parsed struct {
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, &ProviderError{Status: http.StatusOK, Err: fmt.Errorf("decode response: %w", err)}
	}
	return &Response{Status: http.StatusOK, Body: raw, Usage: parsed.Usage}, nil
}

// providerErr maps an SDK error to a ProviderError. API errors keep their
// status and body; transport failures fail over unless the caller
// cancelled.
func providerErr(ctx context.Context, err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return &ProviderError{Status: apiErr.StatusCode, Retryable: retryableStatus(apiErr.StatusCode), Body: []byte(apiErr.RawJSON()), Err: err}
	}
	return &ProviderError{Retryable: ctx.Err() == nil, Err: err}
}
