package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// ProviderClaude is the first-party Claude API.
const ProviderClaude = "claude"

// FamilyAnthropic is the model family of every Claude model, wherever it
// is served.
const FamilyAnthropic = "anthropic"

// Claude provider settings.
const (
	// EnvClaudeAPIKey holds the Claude API key (mounted from the KMS-backed secret).
	EnvClaudeAPIKey = "ANTHROPIC_API_KEY" //nolint:gosec // G101: the environment variable name, not a credential
	// EnvClaudeBaseURL overrides https://api.anthropic.com (tests, private endpoints).
	EnvClaudeBaseURL = "EXEMPLAR_CLAUDE_BASE_URL"
)

var claudeSpec = Spec{
	Family: func(string) string { return FamilyAnthropic },
	ValidateModel: func(model string) error {
		if !strings.HasPrefix(model, "claude-") {
			return errors.New("claude models are named claude-*")
		}
		return nil
	},
	New: func(_ context.Context, s Settings) (Provider, error) {
		key := s.env(EnvClaudeAPIKey)
		if key == "" {
			return nil, errors.New(EnvClaudeAPIKey + " is required")
		}
		opts := []option.RequestOption{option.WithAPIKey(key), option.WithRequestTimeout(10 * time.Minute)}
		if base := s.env(EnvClaudeBaseURL); base != "" {
			opts = append(opts, option.WithBaseURL(base))
		}
		if s.HTTPClient != nil {
			opts = append(opts, option.WithHTTPClient(s.HTTPClient))
		}
		c := anthropic.NewClient(opts...)
		return &claudeProvider{messages: c.Messages}, nil
	},
}

type claudeProvider struct {
	messages anthropic.MessageService
}

func (p *claudeProvider) Name() string { return ProviderClaude }

func (p *claudeProvider) Send(ctx context.Context, req Request) (*Response, error) {
	return sendMessages(ctx, p.messages, req)
}

// The Claude API supports Message Batches; Bedrock does not.
var _ BatchProvider = (*claudeProvider)(nil)

func (p *claudeProvider) CreateBatch(ctx context.Context, body []byte) ([]byte, string, error) {
	var raw []byte
	_, err := p.messages.Batches.New(ctx, anthropic.MessageBatchNewParams{},
		option.WithRequestBody("application/json", bytes.NewReader(body)),
		option.WithResponseBodyInto(&raw),
		option.WithMaxRetries(0), // a retried create could submit the batch twice
	)
	if err != nil {
		return nil, "", providerErr(ctx, err)
	}
	var b struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &b); err != nil || b.ID == "" {
		return nil, "", &ProviderError{Status: http.StatusOK, Err: errNoBatchID}
	}
	return raw, b.ID, nil
}

func (p *claudeProvider) GetBatch(ctx context.Context, id string) ([]byte, error) {
	var raw []byte
	if _, err := p.messages.Batches.Get(ctx, id, anthropic.MessageBatchGetParams{}, option.WithResponseBodyInto(&raw)); err != nil {
		return nil, providerErr(ctx, err)
	}
	return raw, nil
}

func (p *claudeProvider) CancelBatch(ctx context.Context, id string) ([]byte, error) {
	var raw []byte
	if _, err := p.messages.Batches.Cancel(ctx, id, anthropic.MessageBatchCancelParams{}, option.WithResponseBodyInto(&raw)); err != nil {
		return nil, providerErr(ctx, err)
	}
	return raw, nil
}

func (p *claudeProvider) StreamResults(ctx context.Context, id string, fn func(BatchResult) error) error {
	stream := p.messages.Batches.ResultsStreaming(ctx, id, anthropic.MessageBatchResultsParams{})
	defer func() { _ = stream.Close() }()
	for stream.Next() {
		cur := stream.Current()
		res := BatchResult{Raw: []byte(cur.RawJSON()), Succeeded: cur.Result.Type == "succeeded"}
		if res.Succeeded {
			u := cur.Result.Message.Usage
			res.Usage = Usage{
				InputTokens: int(u.InputTokens), OutputTokens: int(u.OutputTokens),
				CacheReadTokens: int(u.CacheReadInputTokens), CacheWriteTokens: int(u.CacheCreationInputTokens),
			}
		}
		if err := fn(res); err != nil {
			return err
		}
	}
	if err := stream.Err(); err != nil {
		return providerErr(ctx, err)
	}
	return nil
}
