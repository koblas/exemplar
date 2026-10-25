package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/koblas/exemplar/internal/llm"
)

// HarnessConfig configures how agents reach the gateway.
type HarnessConfig struct {
	// GatewayURL is exemplar-gateway's base URL.
	GatewayURL string
	// GatewayToken is the shared secret the gateway expects as x-api-key.
	GatewayToken string
	// HTTPClient overrides the transport (tests, scripted models).
	HTTPClient *http.Client
	MaxTurns   int
	MaxTokens  int64
}

// Harness runs tool-using agents through the gateway.
type Harness struct {
	client    anthropic.Client
	maxTurns  int
	maxTokens int64
}

// NewHarness builds a Harness.
func NewHarness(cfg HarnessConfig) *Harness {
	opts := []option.RequestOption{
		option.WithBaseURL(cfg.GatewayURL),
		option.WithAPIKey(cfg.GatewayToken),
		option.WithMaxRetries(1), // failover is the gateway's job
		option.WithRequestTimeout(10 * time.Minute),
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	h := &Harness{client: anthropic.NewClient(opts...), maxTurns: cfg.MaxTurns, maxTokens: cfg.MaxTokens}
	if h.maxTurns <= 0 {
		h.maxTurns = 12
	}
	if h.maxTokens <= 0 {
		h.maxTokens = 16000
	}
	return h
}

// CallMeta identifies an agent's calls to the gateway.
type CallMeta struct {
	Stage     string
	Tier      string
	Caller    string
	Repo      string
	ReviewID  int64
	RuleRunID int64
	// RouteOffset is the verifier's place in a quorum: its call starts at
	// that provider of providers.verifier (llm.HeaderRouteOffset).
	RouteOffset int
}

// AgentSpec is one agent conversation.
type AgentSpec struct {
	Meta CallMeta
	// System is the harness instructions and output contract.
	System string
	// Context is shared by every agent of a review (PR context and diff);
	// it carries the cache breakpoint so agents 2..N read it from cache.
	Context string
	// Task is the per-agent part (rule text, or candidates to verify).
	Task         string
	OutputSchema map[string]any
	Tools        *toolbox
	// DataClasses overrides the declared data classes, which
	// default to diff and pr_text plus file_context once a tool read code.
	DataClasses string
}

// AgentResult is what an agent produced, plus what the harness measured.
type AgentResult struct {
	// Output is the final answer text; it must still be decoded and
	// validated by the caller.
	Output   string
	Calls    []ToolCall
	Usage    Usage
	CostUSD  float64
	Model    string
	Family   string
	Failover bool
	Budget   string
	Turns    int
	Latency  time.Duration
	Status   string // RunOK, RunTimeout, RunSchemaError, RunError
	Detail   string
}

// Run executes the agent loop: the model calls tools until it answers.
func (h *Harness) Run(ctx context.Context, spec AgentSpec) (res AgentResult) { //nolint:nonamedreturns // the deferred func stamps Latency on every return
	start := time.Now()
	res = AgentResult{Status: RunOK}
	defer func() { res.Latency = time.Since(start) }()

	tools := []anthropic.ToolUnionParam{}
	if spec.Tools != nil {
		for _, d := range spec.Tools.defs() {
			tools = append(tools, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
				Name:        d.Name,
				Description: anthropic.String(d.Description),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties:  d.Properties,
					Required:    d.Required,
					ExtraFields: map[string]any{"additionalProperties": false},
				},
				Strict: anthropic.Bool(true),
			}})
		}
	}
	messages := []anthropic.MessageParam{anthropic.NewUserMessage(
		anthropic.ContentBlockParamUnion{OfText: &anthropic.TextBlockParam{Text: spec.Context, CacheControl: anthropic.NewCacheControlEphemeralParam()}},
		anthropic.NewTextBlock(spec.Task),
	)}

	for turn := 1; turn <= h.maxTurns; turn++ {
		res.Turns = turn
		params := anthropic.MessageNewParams{
			Model:     "routed-by-gateway",
			MaxTokens: h.maxTokens,
			System:    []anthropic.TextBlockParam{{Text: spec.System, CacheControl: anthropic.NewCacheControlEphemeralParam()}},
			Messages:  messages,
			Tools:     tools,
		}
		if spec.OutputSchema != nil {
			params.OutputConfig = anthropic.OutputConfigParam{Format: anthropic.JSONOutputFormatParam{Schema: spec.OutputSchema}}
		}
		var httpResp *http.Response
		msg, err := h.client.Messages.New(ctx, params, append(h.metaOptions(spec, spec.Tools), option.WithResponseInto(&httpResp))...)
		if httpResp != nil {
			res.readHeaders(httpResp.Header)
		}
		if err != nil {
			res.Status, res.Detail = RunError, gatewayErrorDetail(err)
			return res
		}
		res.Usage.add(Usage{
			Input: int(msg.Usage.InputTokens), Output: int(msg.Usage.OutputTokens),
			CacheRead: int(msg.Usage.CacheReadInputTokens), CacheWrite: int(msg.Usage.CacheCreationInputTokens),
		})
		messages = append(messages, msg.ToParam())

		switch msg.StopReason {
		case anthropic.StopReasonToolUse:
			if spec.Tools == nil {
				res.Status, res.Detail = RunSchemaError, "model requested a tool but none are available"
				return res
			}
			var results []anthropic.ContentBlockParamUnion
			for _, block := range msg.Content {
				tu, ok := block.AsAny().(anthropic.ToolUseBlock)
				if !ok {
					continue
				}
				out, terr := spec.Tools.run(ctx, tu.Name, json.RawMessage(tu.JSON.Input.Raw()))
				if terr != nil {
					results = append(results, anthropic.NewToolResultBlock(tu.ID, terr.Error(), true))
				} else {
					results = append(results, anthropic.NewToolResultBlock(tu.ID, out, false))
				}
			}
			messages = append(messages, anthropic.NewUserMessage(results...))
			continue
		case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence:
			var b strings.Builder
			for _, block := range msg.Content {
				if t, ok := block.AsAny().(anthropic.TextBlock); ok {
					b.WriteString(t.Text)
				}
			}
			res.Output = b.String()
		case anthropic.StopReasonMaxTokens:
			res.Status, res.Detail = RunSchemaError, "answer truncated at max_tokens"
		case anthropic.StopReasonRefusal:
			res.Status, res.Detail = RunError, "model declined the request"
		case anthropic.StopReasonPauseTurn, anthropic.StopReasonModelContextWindowExceeded:
			res.Status, res.Detail = RunError, "unexpected stop reason "+string(msg.StopReason)
		default:
			res.Status, res.Detail = RunError, "unexpected stop reason "+string(msg.StopReason)
		}
		if spec.Tools != nil {
			res.Calls = spec.Tools.calls()
		}
		return res
	}
	if spec.Tools != nil {
		res.Calls = spec.Tools.calls()
	}
	res.Status, res.Detail = RunTimeout, fmt.Sprintf("no answer within %d tool turns", h.maxTurns)
	return res
}

func (h *Harness) metaOptions(spec AgentSpec, tb *toolbox) []option.RequestOption {
	m := spec.Meta
	classes := "diff,pr_text"
	if spec.DataClasses != "" {
		classes = spec.DataClasses
	} else if tb != nil {
		tb.mu.Lock()
		if tb.usedContext {
			classes += ",file_context"
		}
		tb.mu.Unlock()
	}
	opts := []option.RequestOption{
		option.WithHeader(llm.HeaderStage, m.Stage),
		option.WithHeader(llm.HeaderCaller, m.Caller),
		option.WithHeader(llm.HeaderRepo, m.Repo),
		option.WithHeader(llm.HeaderDataClasses, classes),
	}
	if m.Tier != "" {
		opts = append(opts, option.WithHeader(llm.HeaderTier, m.Tier))
	}
	if m.ReviewID > 0 {
		opts = append(opts, option.WithHeader(llm.HeaderReviewID, strconv.FormatInt(m.ReviewID, 10)))
	}
	if m.RuleRunID > 0 {
		opts = append(opts, option.WithHeader(llm.HeaderRuleRunID, strconv.FormatInt(m.RuleRunID, 10)))
	}
	if m.RouteOffset > 0 {
		opts = append(opts, option.WithHeader(llm.HeaderRouteOffset, strconv.Itoa(m.RouteOffset)))
	}
	return opts
}

func (r *AgentResult) readHeaders(h http.Header) {
	if v := h.Get(llm.HeaderModel); v != "" {
		r.Model = v
	}
	if v := h.Get(llm.HeaderFamily); v != "" {
		r.Family = v
	}
	if h.Get(llm.HeaderFailover) == "true" {
		r.Failover = true
	}
	if v := h.Get(llm.HeaderBudget); v != "" {
		r.Budget = v
	}
	if v, err := strconv.ParseFloat(h.Get(llm.HeaderCostUSD), 64); err == nil {
		r.CostUSD += v
	}
}

// ErrProvidersUnavailable marks a stage whose every provider failed; the
// review is then incomplete.
var ErrProvidersUnavailable = errors.New("engine: every provider for the stage is unavailable")

func gatewayErrorDetail(err error) string {
	if apiErr, ok := errors.AsType[*anthropic.Error](err); ok {
		if apiErr.StatusCode == http.StatusServiceUnavailable {
			return ErrProvidersUnavailable.Error()
		}
		return fmt.Sprintf("gateway returned %d", apiErr.StatusCode)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	return "gateway call failed"
}

// decodeStrict decodes a model answer, rejecting unknown fields and
// trailing data. Schema conformance from the API is a convenience; this is
// the trust boundary.
func decodeStrict(text string, out any) error {
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(text)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode answer: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data after JSON answer", errInvalidAnswer)
	}
	return nil
}
