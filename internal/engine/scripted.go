package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/koblas/exemplar/internal/llm"
)

// Script decides what a scripted model does. It stands in for a model
// that is fully controlled by whatever the PR says (compromised mode in
// the adversarial set) and for live models in tests.
type Script struct {
	// Generator returns the files to open with get_hunk and the raw final
	// answer for a rule or baseline agent. files are the files in scope.
	Generator func(task string, files []string) (reads []string, answer string)
	// Verdict is the verifier's verdict for every candidate: confirmed,
	// refuted or uncertain. Empty means confirmed.
	Verdict string
	// VerdictFor, when set, picks the verdict for each verifier call from
	// its task text (calibration tests: refute some claims, confirm others).
	VerdictFor func(task string) string
	// Categorizer answers risk categorizer calls (stage is categorizer-a
	// or categorizer-b; prContext is the PR as the model sees it). Nil
	// answers medium risk.
	Categorizer func(stage, prContext string) string
	// LinterJudge answers linter-dedupe judge calls (the light stage)
	// from the pair as the model sees it. Nil answers not the same.
	LinterJudge func(pair string) bool
	// HumanJudge answers human-comment dedupe judge calls (also the light
	// stage). Nil answers not the same.
	HumanJudge func(pair string) bool
	// ReplyClass answers reply classifier calls (also the light stage).
	// Nil answers other.
	ReplyClass func(context string) string
	// TeachClass answers teach-the-bot classifier calls. Nil answers noise.
	TeachClass func(context string) string
	// FixedJudge answers fixed-without-suggestion calls. Nil answers no.
	FixedJudge func(context string) bool
	// CategorizerFamily overrides Family per categorizer stage.
	CategorizerFamily map[string]string
	// Quote returns the code at path:line for confirmed evidence.
	Quote func(path string, line int) string
	// Family is reported as the model family (default "scripted").
	Family string
	// Budget is the X-Exemplar-Budget the gateway reports (default ok).
	Budget string
	// Importance is the verifier's importance for every candidate (default 0.9).
	Importance float64
}

// ScriptedTransport answers Messages API calls from a Script. Use it as the
// harness HTTP client's transport in place of the gateway.
type ScriptedTransport struct {
	Script Script

	mu    sync.Mutex
	Calls []ScriptedCall
}

// ScriptedCall records what the harness sent.
type ScriptedCall struct {
	Stage       string
	DataClasses string
	ReviewID    string
	Body        []byte
}

var (
	filesInScopeRE = regexp.MustCompile(`(?m)^- (.+)$`)
	candidateRE    = regexp.MustCompile(`(?m)^\[(\d+)\] (\S+) (RIGHT|LEFT) lines (\d+)-(\d+)$`)
)

// RoundTrip implements http.RoundTripper.
func (s *ScriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	stage := req.Header.Get(llm.HeaderStage)
	s.mu.Lock()
	s.Calls = append(s.Calls, ScriptedCall{Stage: stage, DataClasses: req.Header.Get(llm.HeaderDataClasses), ReviewID: req.Header.Get(llm.HeaderReviewID), Body: body})
	s.mu.Unlock()

	var in struct {
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("scripted: decode request: %w", err)
	}
	if len(in.Messages) == 0 || len(in.Messages[0].Content) < 2 {
		return nil, errors.New("scripted: unexpected request shape")
	}
	var task, prCtx struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(in.Messages[0].Content[1], &task)
	_ = json.Unmarshal(in.Messages[0].Content[0], &prCtx)
	firstTurn := len(in.Messages) == 1

	var content []map[string]any
	stop := "end_turn"
	switch stage {
	case llm.StageVerifier:
		text, verr := s.verdicts(task.Text)
		if verr != nil {
			return nil, verr
		}
		content = []map[string]any{{"type": "text", "text": text}}
	case llm.StageCategorizerA, llm.StageCategorizerB:
		answer := `{"risk": "medium", "p_low": 0.5, "reasons": ["scripted"]}`
		if s.Script.Categorizer != nil {
			answer = s.Script.Categorizer(stage, prCtx.Text)
		}
		content = []map[string]any{{"type": "text", "text": answer}}
	case llm.StageLight:
		if strings.Contains(prCtx.Text, "<untrusted_teach_context>") {
			class := "noise"
			if s.Script.TeachClass != nil {
				class = s.Script.TeachClass(prCtx.Text)
			}
			content = []map[string]any{{"type": "text", "text": fmt.Sprintf(`{"class": %q}`, class)}}
			break
		}
		if strings.Contains(prCtx.Text, "<untrusted_reply_context>") {
			class := "other"
			if s.Script.ReplyClass != nil {
				class = s.Script.ReplyClass(prCtx.Text)
			}
			content = []map[string]any{{"type": "text", "text": fmt.Sprintf(`{"class": %q}`, class)}}
			break
		}
		judge := s.Script.LinterJudge
		if strings.Contains(prCtx.Text, "<untrusted_change_context>") {
			judge = s.Script.FixedJudge
		}
		if strings.Contains(prCtx.Text, "<untrusted_review_context>") {
			judge = s.Script.HumanJudge
		}
		same := judge != nil && judge(prCtx.Text)
		content = []map[string]any{{"type": "text", "text": fmt.Sprintf(`{"same": %t}`, same)}}
	default:
		files := scopedFiles(task.Text)
		reads, answer := s.Script.Generator(task.Text, files)
		if firstTurn && len(reads) > 0 {
			stop = "tool_use"
			for i, p := range reads {
				content = append(content, map[string]any{
					"type": "tool_use", "id": "toolu_" + strconv.Itoa(i), "name": "get_hunk",
					"input": map[string]any{"path": p},
				})
			}
		} else {
			content = []map[string]any{{"type": "text", "text": answer}}
		}
	}
	out, err := json.Marshal(map[string]any{
		"id": "msg_scripted", "type": "message", "role": "assistant", "model": "scripted",
		"content": content, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0},
	})
	if err != nil {
		return nil, fmt.Errorf("scripted: encode response: %w", err)
	}
	family := s.Script.Family
	if f, ok := s.Script.CategorizerFamily[stage]; ok {
		family = f
	}
	if family == "" {
		family = "scripted"
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(llm.HeaderModel, "scripted")
	h.Set(llm.HeaderFamily, family)
	budget := s.Script.Budget
	if budget == "" {
		budget = llm.BudgetOK
	}
	h.Set(llm.HeaderBudget, budget)
	h.Set(llm.HeaderCostUSD, "0")
	return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(bytes.NewReader(out)), Request: req}, nil
}

func (s *ScriptedTransport) verdicts(task string) (string, error) {
	verdict := s.Script.Verdict
	if s.Script.VerdictFor != nil {
		verdict = s.Script.VerdictFor(task)
	}
	if verdict == "" {
		verdict = VerdictConfirmed
	}
	importance := s.Script.Importance
	if importance == 0 {
		importance = 0.9
	}
	matches := candidateRE.FindAllStringSubmatch(task, -1)
	vs := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		idx, _ := strconv.Atoi(m[1])
		start, _ := strconv.Atoi(m[4])
		end, _ := strconv.Atoi(m[5])
		ev := []map[string]any{}
		if verdict == VerdictConfirmed && s.Script.Quote != nil {
			ev = append(ev, map[string]any{"path": m[2], "start_line": start, "end_line": end, "quote": s.Script.Quote(m[2], start)})
		}
		vs = append(vs, map[string]any{"index": idx, "verdict": verdict, "importance": importance, "evidence": ev})
	}
	out, err := json.Marshal(map[string]any{"verdicts": vs})
	if err != nil {
		return "", fmt.Errorf("scripted: encode verdicts: %w", err)
	}
	return string(out), nil
}

// scopedFiles extracts the "- path" lines of a rule or baseline task.
func scopedFiles(task string) []string {
	matches := filesInScopeRE.FindAllStringSubmatch(task, -1)
	files := make([]string, 0, len(matches))
	for _, m := range matches {
		files = append(files, strings.TrimSpace(m[1]))
	}
	return files
}
