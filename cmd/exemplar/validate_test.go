package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	return out.String(), errOut.String(), err
}

func TestValidateExample(t *testing.T) {
	out, _, err := runCLI(t, "validate", "../../examples/exemplar-config")
	if err != nil {
		t.Fatalf("validate example: %v", err)
	}
	if !strings.HasPrefix(out, "ok: ") {
		t.Fatalf("output = %q", out)
	}
}

func TestValidateReportsProblems(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "global.yaml"), []byte("enabled: maybe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runCLI(t, "validate", dir)
	if err == nil {
		t.Fatal("validate succeeded on a broken tree")
	}
	for _, want := range []string{"global.yaml", "platform/eligibility.yaml", "platform/retention.yaml"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not mention %s:\n%s", want, errOut)
		}
	}
}
