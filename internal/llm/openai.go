package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProviderOpenAI is the OpenAI API (Chat Completions). It is the second
// model family behind the gateway: a categorizer side or verifier on it is
// independent of a Claude generator (FR-E10).
const ProviderOpenAI = "openai"

// FamilyOpenAI is the model family of every model the OpenAI API serves.
const FamilyOpenAI = "openai"

// OpenAI provider settings.
const (
	// EnvOpenAIAPIKey holds the OpenAI API key (mounted from the KMS-backed secret).
	EnvOpenAIAPIKey = "OPENAI_API_KEY" //nolint:gosec // G101: the environment variable name, not a credential
	// EnvOpenAIBaseURL overrides https://api.openai.com/v1 (tests, private endpoints).
	EnvOpenAIBaseURL = "EXEMPLAR_OPENAI_BASE_URL"
)

const (
	openAIDefaultBaseURL = "https://api.openai.com/v1"
	// maxOpenAIResponseBytes bounds a response body; answers are capped by
	// max_tokens far below this.
	maxOpenAIResponseBytes = 16 << 20
)

var openAISpec = Spec{
	Family: func(string) string { return FamilyOpenAI },
	ValidateModel: func(model string) error {
		// gpt-* and the o-series reasoning models (o3, o4-mini, ...).
		if strings.HasPrefix(model, "gpt-") || (len(model) > 1 && model[0] == 'o' && model[1] >= '0' && model[1] <= '9') {
			return nil
		}
		return fmt.Errorf("%w: openai models are named gpt-* or o<n>*", errModelName)
	},
	New: func(_ context.Context, s Settings) (Provider, error) {
		key := s.env(EnvOpenAIAPIKey)
		if key == "" {
			return nil, fmt.Errorf("%s %w", EnvOpenAIAPIKey, errRequired)
		}
		base := s.env(EnvOpenAIBaseURL)
		if base == "" {
			base = openAIDefaultBaseURL
		}
		client := s.HTTPClient
		if client == nil {
			client = &http.Client{Timeout: 10 * time.Minute}
		}
		return &openAIProvider{key: key, url: strings.TrimSuffix(base, "/") + "/chat/completions", client: client}, nil
	},
}

// openAIProvider translates the Messages subset Exemplar sends (system
// text plus text, tool_use and tool_result blocks, tools, output_config's JSON
// schema) to Chat Completions and the answer back to Messages JSON, so the
// gateway and its callers see one wire format. It has no Message Batches:
// batch calls route to the next provider in the stage that has them.
type openAIProvider struct {
	key    string
	url    string
	client *http.Client
}

func (p *openAIProvider) Name() string { return ProviderOpenAI }

var (
	errUnsupportedBlock = errors.New("unsupported content block")
	errBadToolArguments = errors.New("tool call arguments are not a JSON object")
	errOpenAIStatus     = errors.New("openai: error status")
)

func (p *openAIProvider) Send(ctx context.Context, req Request) (*Response, error) {
	body, err := toChatCompletion(req.Body)
	if err != nil {
		// Callers send only the subset translated here, so a request
		// outside it is a caller bug: surface it rather than fail over.
		return nil, &ProviderError{Status: http.StatusBadRequest, Body: anthropicError("invalid_request_error", err.Error()), Err: err}
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return nil, &ProviderError{Err: fmt.Errorf("build request: %w", err)}
	}
	hreq.Header.Set("Authorization", "Bearer "+p.key)
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(hreq)
	if err != nil {
		return nil, &ProviderError{Retryable: ctx.Err() == nil, Err: fmt.Errorf("openai: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOpenAIResponseBytes))
	if err != nil {
		return nil, &ProviderError{Status: resp.StatusCode, Retryable: ctx.Err() == nil, Err: fmt.Errorf("openai: read response: %w", err)}
	}
	if resp.StatusCode != http.StatusOK {
		typ, msg := openAIErrorDetail(resp.StatusCode, raw)
		return nil, &ProviderError{
			Status: resp.StatusCode, Retryable: retryableStatus(resp.StatusCode),
			Body: anthropicError(typ, msg), Err: fmt.Errorf("%w %d: %s", errOpenAIStatus, resp.StatusCode, msg),
		}
	}
	out, usage, err := fromChatCompletion(raw)
	if err != nil {
		return nil, &ProviderError{Status: http.StatusOK, Err: fmt.Errorf("openai: %w", err)}
	}
	return &Response{Status: http.StatusOK, Body: out, Usage: usage}, nil
}

// Messages request, as far as the translation reads it.
type msgRequest struct {
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens"`
	System        json.RawMessage `json:"system"`
	Messages      []msgMessage    `json:"messages"`
	Tools         []msgTool       `json:"tools"`
	StopSequences []string        `json:"stop_sequences"`
	Temperature   *float64        `json:"temperature"`
	OutputConfig  *struct {
		Format *struct {
			Type   string         `json:"type"`
			Schema map[string]any `json:"schema"`
		} `json:"format"`
	} `json:"output_config"`
}

type msgMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type msgBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type msgTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	Strict      bool           `json:"strict"`
}

// Chat Completions request.
type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []chatMessage   `json:"messages"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Tools               []chatTool      `json:"tools,omitempty"`
	Stop                []string        `json:"stop,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	ResponseFormat      *chatRespFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Refusal    string         `json:"refusal,omitempty"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters"`
		Strict      bool           `json:"strict,omitempty"`
	} `json:"function"`
}

type chatRespFormat struct {
	Type       string `json:"type"`
	JSONSchema struct {
		Name   string         `json:"name"`
		Schema map[string]any `json:"schema"`
		Strict bool           `json:"strict"`
	} `json:"json_schema"`
}

// toChatCompletion translates a Messages request body to Chat Completions.
// cache_control is dropped: OpenAI caches prompt prefixes on its own.
func toChatCompletion(body []byte) ([]byte, error) {
	var in msgRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("decode messages request: %w", err)
	}
	out := chatRequest{Model: in.Model, MaxCompletionTokens: in.MaxTokens, Stop: in.StopSequences, Temperature: in.Temperature}
	if sys, err := textOf(in.System); err != nil {
		return nil, fmt.Errorf("system: %w", err)
	} else if sys != "" {
		out.Messages = append(out.Messages, chatMessage{Role: "system", Content: &sys})
	}
	for i, m := range in.Messages {
		msgs, err := chatMessages(m)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		out.Messages = append(out.Messages, msgs...)
	}
	for _, t := range in.Tools {
		var ct chatTool
		ct.Type = "function"
		ct.Function.Name, ct.Function.Description, ct.Function.Parameters = t.Name, t.Description, t.InputSchema
		ct.Function.Strict = t.Strict && strictCompatible(t.InputSchema)
		out.Tools = append(out.Tools, ct)
	}
	if oc := in.OutputConfig; oc != nil && oc.Format != nil {
		if oc.Format.Type != "json_schema" {
			return nil, fmt.Errorf("%w: output format %q", errUnsupportedBlock, oc.Format.Type)
		}
		f := &chatRespFormat{Type: "json_schema"}
		f.JSONSchema.Name, f.JSONSchema.Schema = "answer", oc.Format.Schema
		f.JSONSchema.Strict = strictCompatible(oc.Format.Schema)
		out.ResponseFormat = f
	}
	enc, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode chat request: %w", err)
	}
	return enc, nil
}

// chatMessages translates one Messages turn. A user turn's tool_result
// blocks become tool messages, which must directly follow the assistant
// turn that called them, so they come before the turn's text.
func chatMessages(m msgMessage) ([]chatMessage, error) {
	var blocks []msgBlock
	if len(m.Content) > 0 && m.Content[0] == '"' {
		var s string
		if err := json.Unmarshal(m.Content, &s); err != nil {
			return nil, fmt.Errorf("decode content: %w", err)
		}
		blocks = []msgBlock{{Type: "text", Text: s}}
	} else if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil, fmt.Errorf("decode content: %w", err)
	}
	var (
		text  strings.Builder
		calls []chatToolCall
		tools []chatMessage
	)
	for _, b := range blocks {
		switch {
		case b.Type == "text":
			if text.Len() > 0 {
				text.WriteString("\n\n")
			}
			text.WriteString(b.Text)
		case b.Type == "tool_use" && m.Role == "assistant":
			var c chatToolCall
			c.ID, c.Type = b.ID, "function"
			c.Function.Name, c.Function.Arguments = b.Name, string(b.Input)
			calls = append(calls, c)
		case b.Type == "tool_result" && m.Role == "user":
			out, err := textOf(b.Content)
			if err != nil {
				return nil, fmt.Errorf("tool_result %s: %w", b.ToolUseID, err)
			}
			if b.IsError {
				out = "Error: " + out
			}
			tools = append(tools, chatMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: &out})
		default:
			return nil, fmt.Errorf("%w: %s in a %s turn", errUnsupportedBlock, b.Type, m.Role)
		}
	}
	msgs := tools
	if text.Len() > 0 || len(calls) > 0 {
		cm := chatMessage{Role: m.Role, ToolCalls: calls}
		if text.Len() > 0 {
			s := text.String()
			cm.Content = &s
		}
		msgs = append(msgs, cm)
	}
	return msgs, nil
}

// textOf reads a string or an array of text blocks (system, tool_result
// content) as one string.
func textOf(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("decode text: %w", err)
		}
		return s, nil
	}
	var blocks []msgBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("decode text blocks: %w", err)
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type != "text" {
			return "", fmt.Errorf("%w: %s", errUnsupportedBlock, b.Type)
		}
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n\n"), nil
}

// strictCompatible reports whether OpenAI's strict mode accepts schema:
// every object closes additionalProperties and requires all of its
// properties. A schema that doesn't is sent non-strict; callers validate
// answers in code either way.
func strictCompatible(schema any) bool {
	switch s := schema.(type) {
	case map[string]any:
		if props, ok := s["properties"].(map[string]any); ok {
			if ap, ok := s["additionalProperties"].(bool); !ok || ap {
				return false
			}
			required := map[string]bool{}
			switch r := s["required"].(type) {
			case []any:
				for _, n := range r {
					if name, ok := n.(string); ok {
						required[name] = true
					}
				}
			case []string:
				for _, name := range r {
					required[name] = true
				}
			}
			for name, sub := range props {
				if !required[name] || !strictCompatible(sub) {
					return false
				}
			}
		}
		for _, k := range []string{"items", "anyOf", "$defs", "definitions"} {
			if v, ok := s[k]; ok && !strictCompatible(v) {
				return false
			}
		}
		return true
	case []any:
		for _, v := range s {
			if !strictCompatible(v) {
				return false
			}
		}
		return true
	}
	return true
}

// Chat Completions response, as far as the translation reads it.
type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string      `json:"finish_reason"`
		Message      chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

var errNoChoice = errors.New("response has no choices")

// fromChatCompletion translates a Chat Completions response to Messages
// JSON. Usage follows the Messages convention: input excludes cache reads.
func fromChatCompletion(raw []byte) ([]byte, Usage, error) {
	var in chatResponse
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, Usage{}, fmt.Errorf("decode response: %w", err)
	}
	if len(in.Choices) == 0 {
		return nil, Usage{}, errNoChoice
	}
	choice := in.Choices[0]
	content := []map[string]any{}
	if c := choice.Message.Content; c != nil && *c != "" {
		content = append(content, map[string]any{"type": "text", "text": *c})
	}
	for _, tc := range choice.Message.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if len(bytes.TrimSpace(input)) == 0 {
			input = json.RawMessage(`{}`)
		}
		var obj map[string]any
		if err := json.Unmarshal(input, &obj); err != nil {
			return nil, Usage{}, fmt.Errorf("%w: %s", errBadToolArguments, tc.Function.Name)
		}
		content = append(content, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input})
	}
	stop := "end_turn"
	switch {
	case choice.Message.Refusal != "" || choice.FinishReason == "content_filter":
		stop = "refusal"
	case choice.FinishReason == "tool_calls" || len(choice.Message.ToolCalls) > 0:
		stop = "tool_use"
	case choice.FinishReason == "length":
		stop = "max_tokens"
	}
	cached := in.Usage.PromptTokensDetails.CachedTokens
	usage := Usage{InputTokens: in.Usage.PromptTokens - cached, OutputTokens: in.Usage.CompletionTokens, CacheReadTokens: cached}
	out, err := json.Marshal(map[string]any{
		"id": in.ID, "type": "message", "role": "assistant", "model": in.Model,
		"content": content, "stop_reason": stop, "stop_sequence": nil, "usage": usage,
	})
	if err != nil {
		return nil, Usage{}, fmt.Errorf("encode messages response: %w", err)
	}
	return out, usage, nil
}

// openAIErrorDetail maps an OpenAI error response to a Messages error type
// and message, so callers parse one error shape whichever provider failed.
func openAIErrorDetail(status int, raw []byte) (string, string) {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := http.StatusText(status)
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	var typ string
	switch {
	case status == http.StatusUnauthorized:
		typ = "authentication_error"
	case status == http.StatusForbidden:
		typ = "permission_error"
	case status == http.StatusNotFound:
		typ = "not_found_error"
	case status == http.StatusTooManyRequests:
		typ = "rate_limit_error"
	case status >= 500:
		typ = "api_error"
	default:
		typ = "invalid_request_error"
	}
	return typ, msg
}

func anthropicError(typ, msg string) []byte {
	//nolint:errchkjson // a fixed map of strings always encodes
	b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": typ, "message": msg}})
	return b
}
