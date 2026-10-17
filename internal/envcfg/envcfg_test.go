package envcfg

import (
	"errors"
	"strings"
	"testing"
)

func TestPlaceholder(t *testing.T) {
	tests := []struct {
		name, value string
		bad         bool
	}{
		{"empty", "", false},
		{"real repo", "acme/exemplar-config", false},
		{"real URL", "https://exemplar.acme.com", false},
		{"real client id", "Iv23liAbCdEf", false},
		{"core config repo", "example-org/exemplar-config", true},
		{"core git URL", "git@github.com:example-org/exemplar-config.git", true},
		{"core client id", "Iv1.example", true},
		{"core API URL", "https://exemplar.example.invalid", true},
		{"acme blank", "<App ID>", true},
		{"acme blank inside", "x-<installation id>", true},
		{"case", "EXAMPLE-ORG", true},
		{"less than sign", "a < b", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Placeholder("EXEMPLAR_X", tc.value)
			if got := err != nil; got != tc.bad {
				t.Fatalf("Placeholder(%q) = %v, want error %v", tc.value, err, tc.bad)
			}
			if err != nil && (!errors.Is(err, ErrPlaceholder) || !strings.Contains(err.Error(), "EXEMPLAR_X")) {
				t.Fatalf("error %q should wrap ErrPlaceholder and name the setting", err)
			}
		})
	}
}

func TestCheckJoins(t *testing.T) {
	env := map[string]string{"A": "example-org", "B": "fine", "C": "<C>"}
	err := Check(func(k string) string { return env[k] }, "A", "B", "C", "D")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"A ", "C "} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "B ") {
		t.Errorf("error %q names B", err)
	}
	if err := Check(func(string) string { return "ok" }, "A"); err != nil {
		t.Fatalf("Check = %v", err)
	}
}
