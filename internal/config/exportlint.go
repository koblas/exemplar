package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxExportSummary is the longest export.summary allowed.
const MaxExportSummary = 200

// ErrInvalidExportSummary marks an export.summary that fails LintExportSummary.
var ErrInvalidExportSummary = errors.New("invalid export.summary")

var (
	// A sentence break: terminal punctuation followed by whitespace and more
	// text. "context.Context" and "e.g" inside a word do not count.
	sentenceBreakRE = regexp.MustCompile(`[.!?]\s+\S`)
	abbrevRE        = regexp.MustCompile(`(?i)\b(e\.g|i\.e|etc|vs)\.`)
	urlRE           = regexp.MustCompile(`(?i)\b(https?://|www\.)|\b[a-z0-9-]+\.(com|io|net|org|dev|internal|example)\b`)
	// Instructions to an agent beyond the rule itself: a denylist of
	// instruction verbs such as ignore, always run, fetch.
	instructionRE = regexp.MustCompile(`(?i)\b(ignore|disregard|forget|override|bypass|always run|run the|run this|execute|exec|fetch|download|curl|wget|install|upload|send|` +
		`post to|exfiltrate|approve|skip (the )?(review|tests|checks)|disable (the )?(review|tests|checks|linter)|previous instructions|system prompt|you are now|act as)\b`)
)

// LintExportSummary checks an export.summary: at most 200 characters, one
// sentence, no URLs, and no instructions to coding agents beyond the rule.
// Every coding agent in the org reads these lines, so a summary may state
// the rule and nothing else.
func LintExportSummary(s string) error {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return fmt.Errorf("%w: export.summary is empty", ErrInvalidExportSummary)
	case utf8.RuneCountInString(s) > MaxExportSummary:
		return fmt.Errorf("%w: export.summary is %d characters; at most %d", ErrInvalidExportSummary, utf8.RuneCountInString(s), MaxExportSummary)
	case strings.ContainsAny(s, "\n\r"):
		return fmt.Errorf("%w: export.summary must be one line", ErrInvalidExportSummary)
	case sentenceBreakRE.MatchString(abbrevRE.ReplaceAllString(s, "")):
		return fmt.Errorf("%w: export.summary must be one sentence", ErrInvalidExportSummary)
	case urlRE.MatchString(s):
		return fmt.Errorf("%w: export.summary must not contain a URL", ErrInvalidExportSummary)
	}
	if m := instructionRE.FindString(s); m != "" {
		return fmt.Errorf("%w: export.summary may state the rule only; %q reads as an instruction to agents", ErrInvalidExportSummary, m)
	}
	return nil
}
