package feedbacklink

import (
	"errors"
	"strings"
	"testing"
)

func TestSigner(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	s, err := New(key, "https://exemplar.example.invalid/")
	if err != nil {
		t.Fatal(err)
	}
	u := s.URL(8812, "not_important")
	if !strings.HasPrefix(u, "https://exemplar.example.invalid/f/8812/not_important?s=") {
		t.Fatalf("url = %s", u)
	}
	sig := u[strings.Index(u, "?s=")+3:]
	if id, err := s.Verify("8812", "not_important", sig); err != nil || id != 8812 {
		t.Errorf("verify = %d, %v", id, err)
	}
	other, _ := New([]byte(strings.Repeat("x", 32)), "https://exemplar.example.invalid")
	for name, c := range map[string][3]string{
		"another finding": {"8813", "not_important", sig},
		"another reason":  {"8812", "wrong", sig},
		"unknown reason":  {"8812", "approve", s.Sig(8812, "approve")},
		"bad id":          {"x", "wrong", sig},
		"another key":     {"8812", "not_important", other.Sig(8812, "not_important")},
		"empty":           {"8812", "not_important", ""},
	} {
		if _, err := s.Verify(c[0], c[1], c[2]); !errors.Is(err, ErrBadLink) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f := s.Footer(7); strings.Count(f, "](https://") != 5 || !strings.HasPrefix(f, "[Fixed](") || !strings.Contains(f, "[Disagree with rule](") {
		t.Errorf("footer = %s", f)
	}
	if _, err := New([]byte("short"), "https://x"); err == nil {
		t.Error("short key accepted")
	}
	if _, err := New(key, "http://exemplar.example"); err == nil {
		t.Error("plain http accepted")
	}
	if Kind("handled") != "fb_handled" || Kind("x") != "" {
		t.Error("kinds")
	}
}
