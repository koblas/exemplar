package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// Test secrets are assembled at run time so this file never contains a
// literal credential for secret scanners to flag. fakeGitHubToken is
// well-formed and high-entropy, but not a real token.
func fakeGitHubToken() string {
	sum := sha256.Sum256([]byte("exemplar test token"))
	return "ghp_" + hex.EncodeToString(sum[:])[:36]
}

func fakePrivateKey() string {
	return "-----BEGIN " + "RSA PRIVATE KEY-----\n" +
		strings.Repeat("MIIEowIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt0gun\n", 6) +
		"-----END " + "RSA PRIVATE KEY-----\n"
}

func TestRedact(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	tok := fakeGitHubToken()
	in := "package x\n\n// token used in tests\nconst tok = \"" + tok + "\"\nvar again = \"" + tok + "\"\n"

	out, matches, err := s.Redact(in)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no secret detected")
	}
	if strings.Contains(out, tok) {
		t.Fatal("secret survived redaction")
	}
	if strings.Count(out, Placeholder) != 2 {
		t.Errorf("want both occurrences replaced:\n%s", out)
	}
	if matches[0].Line != 4 || matches[0].RuleID == "" {
		t.Errorf("match = %+v, want rule id and line 4", matches[0])
	}
	if got := s.Scan(out); len(got) != 0 {
		t.Errorf("redacted text still scans as secret: %v", got)
	}
}

func TestRedactPrivateKey(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	out, matches, err := s.Redact("cfg:\n" + fakePrivateKey())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if len(matches) == 0 || strings.Contains(out, "PRIVATE KEY-----\nMIIE") {
		t.Fatalf("private key not redacted: %v\n%s", matches, out)
	}
}

func TestCleanTextUnchanged(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	in := "func main() {\n\tfmt.Println(\"hello\")\n}\n"
	out, matches, err := s.Redact(in)
	if err != nil || len(matches) != 0 || out != in {
		t.Fatalf("Redact(clean) = %q, %v, %v", out, matches, err)
	}
}
