package ingest

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Reasons a comment is dropped before it is stored (the code pre-filters,
// plus the data-handling refusals).
const (
	DropBot            = "bot"
	DropSelf           = "self-comment"
	DropShort          = "short"
	DropAck            = "ack" // pure emoji, LGTM and the like
	DropNit            = "nit"
	DropSuggestionOnly = "suggestion-only"
	DropExcluded       = "excluded-path"
	DropSecret         = "unredactable-secret"
)

// Author is a GitHub actor as GraphQL reports it.
type Author struct {
	Typename string `json:"__typename"`
	Login    string `json:"login"`
}

// IsBot reports whether a is an app or bot account, or one of extra (the
// Exemplar app's own login among them).
func IsBot(a Author, extra []string) bool {
	login := strings.ToLower(a.Login)
	return a.Typename == "Bot" || strings.HasSuffix(login, "[bot]") ||
		slices.ContainsFunc(extra, func(b string) bool { return strings.EqualFold(b, a.Login) })
}

var (
	suggestionRE = regexp.MustCompile("(?s)```suggestion[^\n]*\n.*?```")
	acks         = []string{"lgtm", "+1", "ship it", "shipit", "looks good", "looks good to me", "nice", "thanks", "thank you", "done", "ack"}
)

// Prefilter returns why a thread-root comment body is not worth extracting,
// or "" to keep it. It runs on the raw body, before any model sees it.
func Prefilter(body string) string {
	body = strings.TrimSpace(body)
	if strings.Contains(body, "```suggestion") && strings.TrimSpace(suggestionRE.ReplaceAllString(body, "")) == "" {
		return DropSuggestionOnly
	}
	if isAck(body) {
		return DropAck
	}
	if len([]rune(body)) < 25 {
		return DropShort
	}
	lower := strings.ToLower(body)
	if (strings.HasPrefix(lower, "nit:") || strings.HasPrefix(lower, "nit ")) && len(strings.Fields(body)) < 6 {
		return DropNit
	}
	return ""
}

// isAck is true for bodies with no letters or digits (emoji, punctuation)
// or that are only an acknowledgement.
func isAck(body string) bool {
	var b strings.Builder
	for _, r := range strings.ToLower(body) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		}
	}
	words := strings.Join(strings.Fields(b.String()), " ")
	return words == "" || slices.Contains(acks, words)
}
