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
	"strconv"
	"strings"
)

// Env returns git's environment. token may be empty.
func Env(token string) []string { return env(token, false) }

// CheckoutEnv is Env for a throwaway checkout (a review's workspace, a
// test fixture): git's automatic maintenance is off. After a commit or a
// fetch git otherwise starts `git maintenance run --auto` in the
// background, which keeps writing under .git (maintenance.lock, packs)
// while the caller copies the tree or deletes it.
func CheckoutEnv(token string) []string { return env(token, true) }

func env(token string, checkout bool) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=exemplar", "GIT_AUTHOR_EMAIL=exemplar@invalid",
		"GIT_COMMITTER_NAME=exemplar", "GIT_COMMITTER_EMAIL=exemplar@invalid",
	}
	var config [][2]string
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		config = append(config, [2]string{"http.extraHeader", "Authorization: Basic " + basic})
	}
	if checkout {
		config = append(config, [2]string{"maintenance.auto", "false"}, [2]string{"gc.auto", "0"})
	}
	if len(config) > 0 {
		env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
		for i, kv := range config {
			env = append(env, "GIT_CONFIG_KEY_"+strconv.Itoa(i)+"="+kv[0], "GIT_CONFIG_VALUE_"+strconv.Itoa(i)+"="+kv[1])
		}
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
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("git %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("git %s: %w", name, err)
	}
	return out, nil
}
