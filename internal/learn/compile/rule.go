// Package compile turns candidates into deterministic rules:
// the high-tier model writes an ast-grep rule, or a Semgrep rule when the
// pattern needs taint or metavariable comparison, from the candidate's
// description and up to five positive hunks. Code then checks the rule
// (one rule, allowed keys only, the candidate's language), normalizes its
// id, message and severity, validates it with the engine, and runs it in
// a sandbox over the candidate's eval cases. Every attempt is stored in
// compile_attempts with its scores, accepted or not.
//
// A compile is accepted when its precision reaches the exemplar's and its
// recall reaches 90% of the exemplar's. Until eval CI records exemplar
// scores, the bar is global.yaml eval_thresholds, and the attempt records
// which baseline it was held to.
package compile

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
)

// Engines.
const (
	EngineASTGrep = "ast-grep"
	EngineSemgrep = "semgrep"
)

// maxRuleBytes bounds what the model may return.
const maxRuleBytes = 16 << 10

// Language is a candidate's language in each engine's naming.
type Language struct {
	ASTGrep, Semgrep string
	Exts             []string // files of this language, for negative cases
}

var languages = map[string]Language{
	".go":   {"go", "go", []string{".go"}},
	".py":   {"python", "python", []string{".py"}},
	".ts":   {"typescript", "typescript", []string{".ts"}},
	".tsx":  {"tsx", "typescript", []string{".tsx"}},
	".js":   {"javascript", "javascript", []string{".js", ".mjs", ".cjs", ".jsx"}},
	".jsx":  {"javascript", "javascript", []string{".jsx", ".js"}},
	".mjs":  {"javascript", "javascript", []string{".mjs", ".js"}},
	".java": {"java", "java", []string{".java"}},
	".kt":   {"kotlin", "kotlin", []string{".kt", ".kts"}},
	".c":    {"c", "c", []string{".c", ".h"}},
	".h":    {"c", "c", []string{".h", ".c"}},
	".cc":   {"cpp", "cpp", []string{".cc", ".cpp", ".cxx", ".hpp", ".hh", ".h"}},
	".cpp":  {"cpp", "cpp", []string{".cpp", ".cc", ".cxx", ".hpp", ".hh", ".h"}},
	".hpp":  {"cpp", "cpp", []string{".hpp", ".cpp", ".cc", ".h"}},
}

// LanguageFor returns the language for a candidate partition (an
// extension). Language-agnostic candidates ("*") have none: a
// deterministic rule is written for one language.
func LanguageFor(ext string) (Language, bool) {
	l, ok := languages[strings.ToLower(ext)]
	return l, ok
}

// ErrInvalidRule is a rule that fails the checks in code.
var ErrInvalidRule = errors.New("compile: invalid rule")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRule, fmt.Sprintf(format, args...))
}

// Allowed keys. Nothing that rewrites code (fix, rewriters), widens or
// narrows scope (paths: file_patterns own scope) or runs code.
var (
	astGrepKeys = []string{"id", "language", "rule", "constraints", "utils", "transform", "message", "severity", "note"}
	semgrepKeys = []string{"id", "languages", "message", "severity", "mode", "options", "metadata",
		"pattern", "patterns", "pattern-either", "pattern-regex",
		"pattern-sources", "pattern-sinks", "pattern-sanitizers", "pattern-propagators"}
	// forbidden anywhere in the tree.
	forbiddenKeys = []string{"fix", "fix-regex", "rewriters", "paths", "pattern-where-python"}
)

// Normalize checks a model-written rule and rewrites its id, language,
// message and severity, returning the rule body to store and run.
func Normalize(engine, body, ruleID, message string, lang Language) (string, error) {
	if len(body) == 0 || len(body) > maxRuleBytes {
		return "", invalid("rule must be 1-%d bytes", maxRuleBytes)
	}
	if strings.Contains("\n"+body, "\n---") {
		return "", invalid("rule must be a single YAML document")
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		return "", invalid("rule is not YAML: %v", err)
	}
	if k := findKey(doc, forbiddenKeys); k != "" {
		return "", invalid("key %q is not allowed", k)
	}
	message = strings.TrimSpace(message)
	switch engine {
	case EngineASTGrep:
		if err := onlyKeys(doc, astGrepKeys); err != nil {
			return "", err
		}
		if _, ok := doc["rule"].(map[string]any); !ok {
			return "", invalid("ast-grep rule needs a rule: mapping")
		}
		if l, _ := doc["language"].(string); !strings.EqualFold(l, lang.ASTGrep) {
			return "", invalid("language %q, want %q", l, lang.ASTGrep)
		}
		doc["id"], doc["language"], doc["message"], doc["severity"] = ruleID, lang.ASTGrep, message, "warning"
	case EngineSemgrep:
		if err := onlyKeys(doc, []string{"rules"}); err != nil {
			return "", err
		}
		rules, ok := doc["rules"].([]any)
		if !ok || len(rules) != 1 {
			return "", invalid("semgrep rule needs exactly one entry under rules:")
		}
		r, ok := rules[0].(map[string]any)
		if !ok {
			return "", invalid("semgrep rule is not a mapping")
		}
		if err := onlyKeys(r, semgrepKeys); err != nil {
			return "", err
		}
		langs, _ := r["languages"].([]any)
		if len(langs) != 1 || !strings.EqualFold(fmt.Sprint(langs[0]), lang.Semgrep) {
			return "", invalid("languages %v, want [%s]", langs, lang.Semgrep)
		}
		if !slices.ContainsFunc([]string{"pattern", "patterns", "pattern-either", "pattern-regex", "pattern-sinks"}, func(k string) bool { _, ok := r[k]; return ok }) {
			return "", invalid("semgrep rule has no pattern")
		}
		r["id"], r["languages"], r["message"], r["severity"] = ruleID, []string{lang.Semgrep}, message, "WARNING"
	default:
		return "", invalid("engine %q, want ast-grep or semgrep", engine)
	}
	out, err := yaml.MarshalWithOptions(doc, yaml.UseLiteralStyleIfMultiline(true))
	if err != nil {
		return "", invalid("rule cannot be re-encoded: %v", err)
	}
	return string(out), nil
}

func onlyKeys(m map[string]any, allowed []string) error {
	for k := range m {
		if !slices.Contains(allowed, k) {
			return invalid("key %q is not allowed", k)
		}
	}
	return nil
}

// findKey returns the first forbidden key anywhere in v.
func findKey(v any, keys []string) string {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if slices.Contains(keys, k) {
				return k
			}
			if f := findKey(child, keys); f != "" {
				return f
			}
		}
	case []any:
		for _, child := range t {
			if f := findKey(child, keys); f != "" {
				return f
			}
		}
	}
	return ""
}
