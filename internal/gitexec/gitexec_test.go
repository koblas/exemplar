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

// A checkout's git runs no background maintenance, with or without a
// token, and the token keeps its own config entry.
func TestCheckoutEnv(t *testing.T) {
	for _, tok := range []string{"", "ghs_example"} {
		env := CheckoutEnv(tok)
		keys := map[string]string{}
		count := ""
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			if k == "GIT_CONFIG_COUNT" {
				count = v
			}
			if i, ok := strings.CutPrefix(k, "GIT_CONFIG_KEY_"); ok {
				keys[v] = i
			}
		}
		want := "2"
		if tok != "" {
			want = "3"
		}
		if count != want {
			t.Errorf("token %q: GIT_CONFIG_COUNT = %q, want %s", tok, count, want)
		}
		for _, k := range []string{"maintenance.auto", "gc.auto"} {
			i, ok := keys[k]
			if !ok || !slices.Contains(env, "GIT_CONFIG_VALUE_"+i+"="+map[string]string{"maintenance.auto": "false", "gc.auto": "0"}[k]) {
				t.Errorf("token %q: %s not off: %v", tok, k, env)
			}
		}
		if _, ok := keys["http.extraHeader"]; ok != (tok != "") {
			t.Errorf("token %q: extraHeader present %v", tok, ok)
		}
	}
	// git itself reads them: maintenance.auto is off in a checkout's env.
	dir := t.TempDir()
	if _, err := Run(t.Context(), dir, CheckoutEnv(""), "init", "-q"); err != nil {
		t.Fatal(err)
	}
	out, err := Run(t.Context(), dir, CheckoutEnv(""), "config", "--get", "maintenance.auto")
	if err != nil || strings.TrimSpace(string(out)) != "false" {
		t.Errorf("git config maintenance.auto = %q, %v", out, err)
	}
}

func TestRunError(t *testing.T) {
	_, err := Run(t.Context(), t.TempDir(), Env(""), "rev-parse", "--verify", "HEAD")
	if err == nil || !strings.Contains(err.Error(), "git rev-parse") {
		t.Fatalf("err = %v, want it to name the subcommand", err)
	}
}
