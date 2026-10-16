package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/oidc"
)

// `exemplar login` and `exemplar review`: review
// the working tree on exemplar-api with every lane and the verifier,
// before a PR exists. No model key is on the laptop: the CLI signs in
// with a device flow, to GitHub with the review app's client or to the
// OpenID Connect provider with its CLI client (--oidc), and exemplar-api
// checks the token, the caller's access to the repo and their budget.

// credentials is what `exemplar login` saves (0600, in the user config
// directory). EXEMPLAR_TOKEN and EXEMPLAR_API_URL override it.
type credentials struct {
	API   string `json:"api"`
	Token string `json:"token"`
	Login string `json:"login,omitempty"`
	// OIDC sign-ins keep what refreshing the ID token needs.
	Provider      string `json:"provider,omitempty"` // github (default) or oidc
	Refresh       string `json:"refresh_token,omitempty"`
	ClientID      string `json:"client_id,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`
}

func credentialsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "exemplar", "credentials.json"), nil
}

func loadCredentials() (credentials, error) {
	var c credentials
	if p, err := credentialsPath(); err == nil {
		if data, err := os.ReadFile(p); err == nil {
			if err := json.Unmarshal(data, &c); err != nil {
				return c, fmt.Errorf("read %s: %w", p, err)
			}
		}
	}
	if t := os.Getenv("EXEMPLAR_TOKEN"); t != "" {
		c.Token, c.Provider, c.Refresh = t, "", ""
	}
	if u := os.Getenv("EXEMPLAR_API_URL"); u != "" {
		c.API = u
	}
	if c.Token == "" || c.API == "" {
		return c, errors.New("not signed in: run `exemplar login --api https://exemplar.example.com` (or set EXEMPLAR_TOKEN and EXEMPLAR_API_URL)")
	}
	c.API = strings.TrimSuffix(c.API, "/")
	if c.Provider == "oidc" && expiresSoon(c.Token) {
		if c.Refresh == "" || c.TokenEndpoint == "" {
			return c, errors.New("your sign-in expired: run `exemplar login --oidc` again")
		}
		tok, err := refreshOIDC(context.Background(), http.DefaultClient, c)
		if err != nil {
			return c, fmt.Errorf("your sign-in expired and could not be refreshed (%w): run `exemplar login --oidc` again", err)
		}
		c.Token = tok.IDToken
		if tok.RefreshToken != "" {
			c.Refresh = tok.RefreshToken
		}
		if _, err := saveCredentials(c); err != nil {
			return c, err
		}
	}
	return c, nil
}

// expiresSoon reports whether a JWT's exp is within a minute.
func expiresSoon(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return true
	}
	var c struct {
		Exp float64 `json:"exp"`
	}
	if json.Unmarshal(b, &c) != nil {
		return true
	}
	return time.Now().Add(time.Minute).After(time.Unix(int64(c.Exp), 0))
}

func refreshOIDC(ctx context.Context, hc *http.Client, c credentials) (tokenResponse, error) {
	var tok tokenResponse
	if err := postForm(ctx, hc, c.TokenEndpoint, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.Refresh}, "client_id": {c.ClientID}}, &tok); err != nil {
		return tok, err
	}
	if tok.IDToken == "" {
		return tok, fmt.Errorf("no ID token (%s)", tok.Error)
	}
	return tok, nil
}

func saveCredentials(c credentials) (string, error) {
	p, err := credentialsPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	return p, os.WriteFile(p, data, 0o600)
}

func newLoginCmd() *cobra.Command {
	var apiURL, clientID, web, issuer, scopes string
	var useOIDC bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in for `exemplar review` with a device flow (GitHub, or --oidc)",
		Long: "Signs in with a device flow (open the URL shown and enter the code) and saves the token and exemplar-api's URL " +
			"for `exemplar review`, readable only by you. By default it signs in to GitHub with the review app's OAuth client " +
			"($EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID). With --oidc it signs in to the OpenID Connect provider at --oidc-issuer " +
			"($EXEMPLAR_OIDC_ISSUER_URL) with its public CLI client ($EXEMPLAR_OIDC_CLI_CLIENT_ID), and refreshes the ID token " +
			"when it expires (offline_access). The token identifies you to exemplar-api; it is never sent anywhere else.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if useOIDC && !cmd.Flags().Changed("client-id") {
				clientID = os.Getenv("EXEMPLAR_OIDC_CLI_CLIENT_ID")
			}
			if apiURL == "" || clientID == "" {
				return errors.New("--api and --client-id (or EXEMPLAR_API_URL and EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID, or EXEMPLAR_OIDC_CLI_CLIENT_ID with --oidc) are required")
			}
			if !strings.HasPrefix(apiURL, "https://") && !strings.HasPrefix(apiURL, "http://127.0.0.1") {
				return errors.New("--api must be https")
			}
			creds := credentials{API: strings.TrimSuffix(apiURL, "/")}
			if useOIDC {
				if issuer == "" {
					return errors.New("--oidc needs --oidc-issuer (or EXEMPLAR_OIDC_ISSUER_URL)")
				}
				d, err := oidc.Discover(ctx, http.DefaultClient, issuer)
				if err != nil {
					return err
				}
				if d.DeviceEndpoint == "" {
					return errors.New("the OIDC provider does not offer the device flow (no device_authorization_endpoint)")
				}
				tok, err := deviceFlowAt(ctx, http.DefaultClient, d.DeviceEndpoint, d.TokenEndpoint,
					url.Values{"client_id": {clientID}, "scope": {strings.Join(strings.Split(scopes, ","), " ")}}, cmd.ErrOrStderr(), loginPoll)
				if err != nil {
					return err
				}
				if tok.IDToken == "" {
					return errors.New("the OIDC provider returned no ID token (is openid in --scopes?)")
				}
				creds.Provider, creds.Token, creds.Refresh, creds.ClientID, creds.TokenEndpoint = "oidc", tok.IDToken, tok.RefreshToken, clientID, d.TokenEndpoint
			} else {
				token, err := deviceFlow(ctx, http.DefaultClient, strings.TrimSuffix(web, "/")+"/", clientID, cmd.ErrOrStderr(), loginPoll)
				if err != nil {
					return err
				}
				creds.Token = token
			}
			p, err := saveCredentials(creds)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Signed in. Saved to %s.\n", p)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api", os.Getenv("EXEMPLAR_API_URL"), "exemplar-api's URL")
	cmd.Flags().StringVar(&clientID, "client-id", os.Getenv("EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID"), "the OAuth client id (GitHub's, or the OIDC CLI client with --oidc)")
	cmd.Flags().StringVar(&web, "github", "https://github.com/", "GitHub's web URL (GHES: https://<host>/)")
	cmd.Flags().BoolVar(&useOIDC, "oidc", false, "sign in to the OpenID Connect provider instead of GitHub")
	cmd.Flags().StringVar(&issuer, "oidc-issuer", os.Getenv("EXEMPLAR_OIDC_ISSUER_URL"), "the OIDC issuer URL")
	cmd.Flags().StringVar(&scopes, "scopes", "openid,profile,email,offline_access", "OIDC scopes, comma-separated")
	return cmd
}

// loginPoll overrides the providers' device-flow polling interval (tests).
var loginPoll time.Duration

// tokenResponse is an OAuth token endpoint's answer.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	Interval     int    `json:"interval"`
}

// deviceFlow runs GitHub's OAuth device flow and returns the user token.
// interval overrides GitHub's polling interval (tests).
func deviceFlow(ctx context.Context, c *http.Client, web, clientID string, out io.Writer, interval time.Duration) (string, error) {
	tok, err := deviceFlowAt(ctx, c, web+"login/device/code", web+"login/oauth/access_token", url.Values{"client_id": {clientID}}, out, interval)
	if err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", errors.New("token: GitHub returned none")
	}
	return tok.AccessToken, nil
}

// deviceFlowAt runs the OAuth device authorization grant (RFC 8628):
// it shows the user code and polls the token endpoint until the person
// approves. form carries client_id (and scope).
func deviceFlowAt(ctx context.Context, c *http.Client, codeURL, tokenURL string, form url.Values, out io.Writer, interval time.Duration) (tokenResponse, error) {
	var code struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := postForm(ctx, c, codeURL, form, &code); err != nil {
		return tokenResponse{}, fmt.Errorf("device code: %w", err)
	}
	if code.DeviceCode == "" || code.UserCode == "" {
		return tokenResponse{}, errors.New("device code: none returned (is device flow enabled for the client?)")
	}
	fmt.Fprintf(out, "Open %s and enter the code %s\n", code.VerificationURI, code.UserCode)
	if code.VerificationURIComplete != "" {
		fmt.Fprintf(out, "(or open %s)\n", code.VerificationURIComplete)
	}
	wait := time.Duration(max(code.Interval, 5)) * time.Second
	if interval > 0 {
		wait = interval
	}
	deadline := time.Now().Add(time.Duration(max(code.ExpiresIn, 60)) * time.Second)
	poll := url.Values{"client_id": {form.Get("client_id")}, "device_code": {code.DeviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return tokenResponse{}, ctx.Err()
		case <-time.After(wait):
		}
		var tok tokenResponse
		if err := postForm(ctx, c, tokenURL, poll, &tok); err != nil {
			return tokenResponse{}, fmt.Errorf("token: %w", err)
		}
		switch tok.Error {
		case "":
			return tok, nil
		case "authorization_pending":
		case "slow_down":
			if interval == 0 {
				wait = time.Duration(max(tok.Interval, int(wait/time.Second)+5)) * time.Second
			}
		default:
			return tokenResponse{}, fmt.Errorf("sign-in failed: %s", tok.Error)
		}
	}
	return tokenResponse{}, errors.New("sign-in timed out; run `exemplar login` again")
}

func postForm(ctx context.Context, c *http.Client, u string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	// An OAuth error (authorization_pending, …) comes back as 400 with a
	// JSON body (RFC 6749 §5.2); GitHub answers 200.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	return nil
}

func newReviewCmd() *cobra.Command {
	var (
		base, repo, format, agent string
		wait, poll                time.Duration
		exitZero                  bool
	)
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Review the working tree before opening a PR (spec §10.1)",
		Long: "Sends the diff of the working tree (tracked changes and untracked files that are not ignored) against its " +
			"merge-base with --base to exemplar-api, which reviews it on that pushed commit with every lane and the " +
			"verifier, as a PR review would, and prints what would have been posted. Nothing is posted anywhere. Exits 1 " +
			"when there are findings (0 with --exit-zero). Coding agents can run it before opening a PR; pass --agent to " +
			"say which. Sign in first with `exemplar login`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			creds, err := loadCredentials()
			if err != nil {
				return err
			}
			if repo == "" {
				if repo, err = originRepo(ctx); err != nil {
					return err
				}
			}
			if base == "" {
				base = defaultBase(ctx)
			}
			baseSHA, err := runGit(ctx, "merge-base", "HEAD", base)
			if err != nil {
				return fmt.Errorf("merge-base with %s: %w (set --base to a pushed branch)", base, err)
			}
			diff, err := workingDiff(ctx, baseSHA)
			if err != nil {
				return err
			}
			if diff == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing to review: the working tree matches "+base+".")
				return nil
			}
			req := evaluate.Request{Repo: repo, BaseSHA: baseSHA, Diff: diff, Agent: agent}
			if err := req.Validate(); err != nil {
				return err
			}
			c := &http.Client{Timeout: 30 * time.Second}
			var st evaluate.Status
			if err := apiCall(ctx, c, creds, http.MethodPost, "/v1/evaluate", req, &st); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Reviewing %s against %s (%s)...\n", repo, base, baseSHA[:12])
			deadline := time.Now().Add(wait)
			for !st.Final() {
				if time.Now().After(deadline) {
					return fmt.Errorf("the review is still running after %s; its id is %s", wait, st.ID)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(poll):
				}
				if err := apiCall(ctx, c, creds, http.MethodGet, "/v1/evaluate/"+st.ID, nil, &st); err != nil {
					return err
				}
			}
			switch st.Status {
			case evaluate.StatusRefused:
				return errors.New("not reviewed: " + st.Reason)
			case evaluate.StatusFailed:
				return errors.New(st.Reason)
			}
			res := st.Result
			if res == nil {
				res = &evaluate.Result{}
			}
			if format == "sarif" {
				data, err := reviewSARIF(res.Findings)
				if err != nil {
					return err
				}
				_, _ = cmd.OutOrStdout().Write(append(data, '\n'))
			} else {
				printFindings(cmd.OutOrStdout(), res, st.CostUSD)
			}
			if res.Incomplete != "" {
				return errors.New("the review did not complete: " + res.Incomplete)
			}
			if len(res.Findings) > 0 && !exitZero {
				return fmt.Errorf("%d finding(s)", len(res.Findings))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "the branch to diff against (default: origin's default branch)")
	cmd.Flags().StringVar(&repo, "repo", "", "owner/name (default: from the origin remote)")
	cmd.Flags().StringVar(&format, "format", "text", "text or sarif")
	cmd.Flags().StringVar(&agent, "agent", os.Getenv("EXEMPLAR_AGENT"), "the coding agent running the review, if any (e.g. claude_code)")
	cmd.Flags().DurationVar(&wait, "wait", 15*time.Minute, "how long to wait for the review")
	cmd.Flags().DurationVar(&poll, "poll", 5*time.Second, "how often to check on it")
	cmd.Flags().BoolVar(&exitZero, "exit-zero", false, "exit 0 even with findings")
	return cmd
}

func runGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

var remoteRE = regexp.MustCompile(`(?:github\.com|[^/:@]+)[:/]([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

func originRepo(ctx context.Context) (string, error) {
	u, err := runGit(ctx, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("%w (or pass --repo owner/name)", err)
	}
	m := remoteRE.FindStringSubmatch(u)
	if m == nil {
		return "", fmt.Errorf("cannot read owner/name from origin %q; pass --repo", u)
	}
	return m[1], nil
}

// defaultBase is origin's default branch, as the clone recorded it.
func defaultBase(ctx context.Context) string {
	if ref, err := runGit(ctx, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
		return ref
	}
	return "origin/main"
}

// workingDiff is the working tree against base: tracked changes, staged
// or not, and untracked files git does not ignore.
func workingDiff(ctx context.Context, base string) (string, error) {
	flags := []string{"--no-color", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/"}
	d, err := rawGit(ctx, append(append([]string{"diff"}, flags...), base)...)
	if err != nil {
		return "", err
	}
	untracked, err := rawGit(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(d)
	for _, p := range strings.Split(untracked, "\x00") {
		if p == "" {
			continue
		}
		// --no-index exits 1 when the files differ, which they do.
		out, err := rawGit(ctx, append(append([]string{"diff", "--no-index"}, flags...), "--", "/dev/null", p)...)
		var ee *exec.ExitError
		if err != nil && (!errors.As(err, &ee) || ee.ExitCode() != 1) {
			return "", err
		}
		b.WriteString(out)
	}
	return b.String(), nil
}

func rawGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	return string(out), err
}

func apiCall(ctx context.Context, c *http.Client, creds credentials, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, creds.API+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "exemplar-cli/"+version)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("exemplar-api: %s", e.Error)
		}
		return fmt.Errorf("exemplar-api: %s", resp.Status)
	}
	return json.Unmarshal(data, out)
}

func printFindings(w io.Writer, res *evaluate.Result, cost float64) {
	for _, f := range res.Findings {
		lines := fmt.Sprint(f.StartLine)
		if f.EndLine > f.StartLine {
			lines += fmt.Sprintf("-%d", f.EndLine)
		}
		fmt.Fprintf(w, "%s:%s: %s [%s] %s\n", f.Path, lines, f.Severity, f.RuleID, f.Message)
		if f.Suggestion != "" {
			for _, l := range strings.Split(strings.TrimRight(f.Suggestion, "\n"), "\n") {
				fmt.Fprintln(w, "    | "+l)
			}
		}
	}
	summary := fmt.Sprintf("%d finding(s) from %d rule(s), coverage %.0f%%", len(res.Findings), res.Rules, 100*res.Coverage)
	if res.HeldBack > 0 {
		summary += fmt.Sprintf(", %d more held back by the comment budget", res.HeldBack)
	}
	if res.Degraded {
		summary += ", degraded"
	}
	fmt.Fprintf(w, "%s ($%.2f).\n", summary, cost)
}

// reviewSARIF renders findings as SARIF 2.1.0 for editors and CI.
func reviewSARIF(fs []evaluate.Finding) ([]byte, error) {
	type region struct {
		StartLine int `json:"startLine"`
		EndLine   int `json:"endLine"`
	}
	type location struct {
		Physical struct {
			Artifact struct {
				URI string `json:"uri"`
			} `json:"artifactLocation"`
			Region region `json:"region"`
		} `json:"physicalLocation"`
	}
	type result struct {
		RuleID    string            `json:"ruleId"`
		Level     string            `json:"level"`
		Message   map[string]string `json:"message"`
		Locations []location        `json:"locations"`
	}
	level := map[string]string{"blocking": "error", "important": "warning", "suggestion": "note"}
	rules := []map[string]any{}
	seen := map[string]bool{}
	results := []result{}
	for _, f := range fs {
		if !seen[f.RuleID] {
			seen[f.RuleID] = true
			rules = append(rules, map[string]any{"id": f.RuleID, "shortDescription": map[string]string{"text": f.RuleID}})
		}
		var loc location
		loc.Physical.Artifact.URI = f.Path
		loc.Physical.Region = region{f.StartLine, f.EndLine}
		lv := level[f.Severity]
		if lv == "" {
			lv = "note"
		}
		results = append(results, result{RuleID: f.RuleID, Level: lv, Message: map[string]string{"text": f.Message}, Locations: []location{loc}})
	}
	return json.Marshal(map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json", "version": "2.1.0",
		"runs": []any{map[string]any{"tool": map[string]any{"driver": map[string]any{"name": "Exemplar", "rules": rules}}, "results": results}},
	})
}
