package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/evaluate"
)

func TestDeviceFlow(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "Iv1.test" {
			t.Errorf("client id = %q", r.Form.Get("client_id"))
		}
		switch r.URL.Path {
		case "/login/device/code":
			_, _ = w.Write([]byte(`{"device_code": "dc", "user_code": "ABCD-1234", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5}`))
		case "/login/oauth/access_token":
			polls++
			if r.Form.Get("device_code") != "dc" || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Errorf("poll form = %v", r.Form)
			}
			switch polls {
			case 1:
				_, _ = w.Write([]byte(`{"error": "authorization_pending"}`))
			case 2:
				_, _ = w.Write([]byte(`{"error": "slow_down", "interval": 10}`))
			default:
				_, _ = w.Write([]byte(`{"access_token": "ghu_token"}`))
			}
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	tok, err := deviceFlow(t.Context(), srv.Client(), srv.URL+"/", "Iv1.test", &out, time.Millisecond)
	if err != nil || tok != "ghu_token" || polls != 3 || !strings.Contains(out.String(), "ABCD-1234") {
		t.Fatalf("token %q, %v, polls %d, out %q", tok, err, polls, out.String())
	}
	// A denial is an error.
	polls = 100
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/device/code" {
			_, _ = w.Write([]byte(`{"device_code": "dc", "user_code": "X", "verification_uri": "u", "expires_in": 900}`))
			return
		}
		_, _ = w.Write([]byte(`{"error": "access_denied"}`))
	}))
	defer deny.Close()
	if _, err := deviceFlow(t.Context(), deny.Client(), deny.URL+"/", "Iv1.test", io.Discard, time.Millisecond); err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("denied = %v", err)
	}
}

func TestRemoteRepo(t *testing.T) {
	for u, want := range map[string]string{
		"git@github.com:acme/app.git":         "acme/app",
		"https://github.com/acme/app.git":     "acme/app",
		"https://github.com/acme/app":         "acme/app",
		"ssh://git@ghe.example.com/acme/app/": "acme/app",
		"https://ghe.example.com/acme/my.app": "acme/my.app",
	} {
		m := remoteRE.FindStringSubmatch(u)
		if m == nil || m[1] != want {
			t.Errorf("%s: %v", u, m)
		}
	}
}

func TestReviewCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "base")
	base := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "remote", "add", "origin", "git@github.com:acme/app.git")
	git(t, dir, "checkout", "-q", "-b", "feature")
	// A tracked change, an untracked file, and an ignored one.
	for name, body := range map[string]string{"a.go": "package a\n\nfunc A() {}\n", "b.go": "package a\n", ".gitignore": "secret.txt\n", "secret.txt": "x\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", ".gitignore")

	var mu sync.Mutex
	var got evaluate.Request
	polls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer ghu_test" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error": "sign in"}`))
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/evaluate":
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id": "e1", "status": "queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/evaluate/e1":
			polls++
			if polls < 2 {
				_, _ = w.Write([]byte(`{"id": "e1", "status": "running"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(evaluate.Status{ID: "e1", Status: evaluate.StatusDone, CostUSD: 0.12, Result: &evaluate.Result{
				Rules: 4, Coverage: 1, Findings: []evaluate.Finding{{
					RuleID: "propagate-context", Severity: "important", Path: "a.go", StartLine: 3, EndLine: 3,
					Message: "Pass ctx.", Suggestion: "func A(ctx context.Context) {}",
				}},
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	t.Setenv("EXEMPLAR_TOKEN", "ghu_test")
	t.Setenv("EXEMPLAR_API_URL", api.URL)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Chdir(dir)

	run := func(args ...string) (string, error) {
		root := newRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(io.Discard)
		root.SetArgs(args)
		err := root.ExecuteContext(t.Context())
		return out.String(), err
	}
	if _, err := run("review", "--base", "main", "--poll", "soon"); err == nil || !strings.Contains(err.Error(), "invalid argument") {
		t.Fatalf("bad flag accepted: %v", err)
	}
	out, err := run("review", "--base", "main", "--poll", "1ms", "--agent", "claude_code")
	if err == nil || err.Error() != "1 finding(s)" {
		t.Fatalf("review = %q, %v", out, err)
	}
	if got.Repo != "acme/app" || got.BaseSHA != base || got.Agent != "claude_code" ||
		!strings.Contains(got.Diff, "diff --git a/a.go b/a.go") || !strings.Contains(got.Diff, "diff --git a/b.go b/b.go") ||
		!strings.Contains(got.Diff, "+func A() {}") || strings.Contains(got.Diff, "b/secret.txt") {
		t.Errorf("request = %+v", got)
	}
	if !strings.Contains(out, "a.go:3: important [propagate-context] Pass ctx.") || !strings.Contains(out, "    | func A(ctx context.Context) {}") ||
		!strings.Contains(out, "1 finding(s) from 4 rule(s), coverage 100% ($0.12).") {
		t.Errorf("out = %q", out)
	}

	polls = 5
	out, err = run("review", "--base", "main", "--poll", "1ms", "--format", "sarif", "--exit-zero")
	if err != nil {
		t.Fatal(err)
	}
	var log struct {
		Version string `json:"version"`
		Runs    []struct {
			Results []struct {
				RuleID string `json:"ruleId"`
				Level  string `json:"level"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &log); err != nil || log.Version != "2.1.0" || len(log.Runs[0].Results) != 1 || log.Runs[0].Results[0].Level != "warning" {
		t.Errorf("sarif = %s, %v", out, err)
	}

	// No changes: nothing is sent.
	git(t, dir, "reset", "-q", "--hard")
	git(t, dir, "clean", "-qfdx")
	got = evaluate.Request{}
	if out, err := run("review", "--base", "main"); err != nil || !strings.Contains(out, "Nothing to review") || got.Repo != "" {
		t.Errorf("clean tree = %q, %v", out, err)
	}

	// Not signed in.
	t.Setenv("EXEMPLAR_TOKEN", "")
	if _, err := run("review", "--base", "main"); err == nil || !strings.Contains(err.Error(), "exemplar login") {
		t.Errorf("signed out = %v", err)
	}
}

func TestLoginOIDC(t *testing.T) {
	jwt := func(exp time.Time) string {
		enc := func(v any) string {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			return base64.RawURLEncoding.EncodeToString(b)
		}
		return enc(map[string]string{"alg": "RS256"}) + "." + enc(map[string]any{"sub": "u1", "exp": exp.Unix()}) + ".sig"
	}
	polls, refreshed := 0, 0
	var idp *httptest.Server
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": idp.URL, "authorization_endpoint": idp.URL + "/a", "token_endpoint": idp.URL + "/token",
				"jwks_uri": idp.URL + "/jwks", "device_authorization_endpoint": idp.URL + "/device",
			})
		case "/device":
			if r.Form.Get("client_id") != "cli" || !strings.Contains(r.Form.Get("scope"), "offline_access") {
				t.Errorf("device form = %v", r.Form)
			}
			_, _ = w.Write([]byte(`{"device_code": "dc", "user_code": "WXYZ", "verification_uri": "https://idp/activate", "expires_in": 600}`))
		case "/token":
			switch r.Form.Get("grant_type") {
			case "urn:ietf:params:oauth:grant-type:device_code":
				polls++
				if polls == 1 {
					w.WriteHeader(http.StatusBadRequest) // RFC 8628: pending is a 400
					_, _ = w.Write([]byte(`{"error": "authorization_pending"}`))
					return
				}
				// An ID token that has already expired, to exercise refresh.
				_ = json.NewEncoder(w).Encode(map[string]string{"id_token": jwt(time.Now().Add(-time.Minute)), "refresh_token": "r1"})
			case "refresh_token":
				refreshed++
				if r.Form.Get("refresh_token") != "r1" || r.Form.Get("client_id") != "cli" {
					t.Errorf("refresh form = %v", r.Form)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"id_token": jwt(time.Now().Add(time.Hour)), "refresh_token": "r2"})
			}
		}
	}))
	defer idp.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("EXEMPLAR_TOKEN", "")
	t.Setenv("EXEMPLAR_API_URL", "")
	t.Setenv("EXEMPLAR_OIDC_CLI_CLIENT_ID", "cli")
	loginPoll = time.Millisecond
	defer func() { loginPoll = 0 }()
	out, errOut, err := runCLI(t, "login", "--oidc", "--oidc-issuer", idp.URL, "--api", "https://exemplar.example")
	if err != nil || !strings.Contains(out, "Signed in") || !strings.Contains(errOut, "WXYZ") || polls != 2 {
		t.Fatalf("login = %q %q %v (polls %d)", out, errOut, err, polls)
	}
	// The stored ID token has expired: loading refreshes and saves it.
	c, err := loadCredentials(t.Context())
	if err != nil || c.Provider != "oidc" || refreshed != 1 || c.Refresh != "r2" || expiresSoon(c.Token) {
		t.Fatalf("credentials = %+v, %v (refreshed %d)", c, err, refreshed)
	}
	if c, err := loadCredentials(t.Context()); err != nil || refreshed != 1 || c.Refresh != "r2" {
		t.Errorf("a fresh token was refreshed again: %+v %v", c, err)
	}
	if _, _, err := runCLI(t, "login", "--oidc", "--api", "https://exemplar.example", "--oidc-issuer", ""); err == nil {
		t.Error("--oidc without an issuer accepted")
	}
}

// Model text must not reach the terminal as escape sequences or fake
// extra findings.
func TestPrintFindingsStripsControls(t *testing.T) {
	res := &evaluate.Result{Rules: 1, Coverage: 1, Findings: []evaluate.Finding{{
		Path: "a.go\x1b[2K", StartLine: 3, EndLine: 3, Severity: "suggestion", RuleID: "r\u009b1",
		Message:    "set a timeout\x1b]0;pwned\x07\nb.go:1: blocking [fake] injected\r",
		Suggestion: "client := &http.Client{\x1b[31mTimeout: 5 * time.Second}\n\tok\x00",
	}}}
	var buf bytes.Buffer
	printFindings(&buf, res, 0)
	out := buf.String()
	for _, r := range out {
		if (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f) {
			t.Fatalf("output has control character %U:\n%q", r, out)
		}
	}
	if strings.Contains(out, "\nb.go:1:") {
		t.Errorf("message newline started a fake finding line:\n%s", out)
	}
	if !strings.Contains(out, "    | \tok\n") {
		t.Errorf("suggestion lost its tab or lines:\n%q", out)
	}
}

func TestTerminalText(t *testing.T) {
	if got := terminalText("a\x1b[31mb\u009bc\n\td\r\x00"); got != "a[31mbc\n\td" {
		t.Errorf("terminalText = %q", got)
	}
}
