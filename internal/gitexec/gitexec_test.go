package gitexec

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

func TestEnv(t *testing.T) {
	plain := Env("")
	if slices.ContainsFunc(plain, func(s string) bool { return strings.HasPrefix(s, "GIT_CONFIG_COUNT") }) {
		t.Error("no-token env sets extra config")
	}
	for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"} {
		if !slices.Contains(plain, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	tok := "ghs_example"
	env := Env(tok)
	header := "GIT_CONFIG_VALUE_0=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))
	if !slices.Contains(env, header) || slices.ContainsFunc(env, func(s string) bool { return strings.Contains(s, tok) }) {
		t.Errorf("token must appear only base64-encoded in the header: %v", env)
	}
}

func TestRunError(t *testing.T) {
	_, err := Run(t.Context(), t.TempDir(), Env(""), "rev-parse", "--verify", "HEAD")
	if err == nil || !strings.Contains(err.Error(), "git rev-parse") {
		t.Fatalf("err = %v, want it to name the subcommand", err)
	}
}
