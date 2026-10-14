// Package gitexec runs the git binary in an isolated environment: no user
// or system config, no prompts, and an installation token passed as an
// HTTP header through GIT_CONFIG_* so it never appears in argv, URLs or
// the repo's config.
package gitexec

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Env returns git's environment. token may be empty.
func Env(token string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=exemplar", "GIT_AUTHOR_EMAIL=exemplar@invalid",
		"GIT_COMMITTER_NAME=exemplar", "GIT_COMMITTER_EMAIL=exemplar@invalid",
	}
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic,
		)
	}
	return env
}

// Run runs git with args in dir (when set) and returns stdout. Errors
// carry git's stderr, which never contains the token.
func Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		name := args[0]
		if dir != "" && len(args) > 2 {
			name = args[2]
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("git %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("git %s: %w", name, err)
	}
	return out, nil
}
