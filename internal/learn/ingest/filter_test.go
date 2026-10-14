package ingest

import (
	"slices"
	"testing"
)

func TestPrefilter(t *testing.T) {
	tests := map[string]string{
		"LGTM":                                 DropAck,
		"lgtm!! 🎉":                             DropAck,
		"👍👍":                                   DropAck,
		"+1":                                   DropAck,
		"rename this":                          DropShort,
		"nit: trailing whitespace here please": DropNit,
		"nit: this name hides the package-level logger, which confuses readers":                                            "",
		"```suggestion\nreturn nil\n```":                                                                                   DropSuggestionOnly,
		"```suggestion\nreturn nil\n```\nThe error is already logged by the caller, so returning it twice double-reports.": "",
		"This fetch runs once per item; batch it outside the loop instead.":                                                "",
	}
	for body, want := range tests {
		if got := Prefilter(body); got != want {
			t.Errorf("Prefilter(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestIsBot(t *testing.T) {
	tests := []struct {
		a    Author
		want bool
	}{
		{Author{"Bot", "dependabot"}, true},
		{Author{"User", "renovate[bot]"}, true},
		{Author{"User", "Exemplar-Review"}, true},
		{Author{"User", "alice"}, false},
	}
	for _, tt := range tests {
		if got := IsBot(tt.a, []string{"exemplar-review"}); got != tt.want {
			t.Errorf("IsBot(%v) = %v", tt.a, got)
		}
	}
}

func TestPseudonymizer(t *testing.T) {
	if _, err := NewPseudonymizer([]byte("short")); err == nil {
		t.Fatal("accepted a short key")
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	p, _ := NewPseudonymizer(key)
	if p.PID("Alice") != p.PID("alice") {
		t.Error("pseudonyms are case-sensitive")
	}
	if p.PID("alice") == p.PID("bob") || len(p.PID("alice")) != 64 {
		t.Errorf("bad pseudonym %q", p.PID("alice"))
	}
	other, _ := NewPseudonymizer([]byte("ffffffffffffffffffffffffffffffff"))
	if other.PID("alice") == p.PID("alice") {
		t.Error("pseudonym does not depend on the key")
	}
}

func TestDocURLs(t *testing.T) {
	body := "See https://docs.internal.example/http/timeouts#client, and " +
		"<https://wiki.acme.example/x/Retries?src=pr> or http://github.com/acme/style-guide/blob/main/go.md. " +
		"Not https://stackoverflow.com/q/1 nor https://github.com/other/repo. Again: https://docs.internal.example/http/timeouts"
	got := DocURLs(body, []string{"docs.internal.example", "acme.example", "github.com/acme/style-guide"})
	want := []string{
		"https://docs.internal.example/http/timeouts",
		"https://wiki.acme.example/x/Retries",
		"https://github.com/acme/style-guide/blob/main/go.md",
	}
	if !slices.Equal(got, want) {
		t.Errorf("DocURLs = %v, want %v", got, want)
	}
	if got := DocURLs("no links", nil); got == nil || len(got) != 0 {
		t.Errorf("DocURLs with no links = %#v, want empty non-nil", got)
	}
}
