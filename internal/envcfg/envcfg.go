// Package envcfg holds the startup checks the binaries share for settings
// read from the environment: placeholder values left over from the example
// manifests, and the keys derived from the installation's root key.
package envcfg

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrPlaceholder marks a setting that still holds a value from the example
// manifests (deploy/k8s/core, examples/k8s/acme).
var ErrPlaceholder = errors.New("is a placeholder from the example manifests")

// placeholders are substrings that only occur in values the manifests ship
// for the operator to replace. They are matched case-insensitively.
var placeholders = []string{
	"example-org",       // core settings.yaml: org, config repo, git URL
	"iv1.example",       // core settings.yaml: OAuth client id
	"example.invalid",   // core settings.yaml: API URL
	"exemplar.example.", // README examples
}

// angle matches the acme overlay's "<App ID>"-style blanks.
var angle = regexp.MustCompile(`<[^<>]+>`)

// Placeholder returns an error naming the setting when v is a value the
// example manifests ship for the operator to replace. Empty values pass:
// whether a setting is required is the caller's check.
func Placeholder(name, v string) error {
	if v == "" {
		return nil
	}
	if angle.MatchString(v) {
		return fmt.Errorf("%s %q %w", name, v, ErrPlaceholder)
	}
	lower := strings.ToLower(v)
	for _, p := range placeholders {
		if strings.Contains(lower, p) {
			return fmt.Errorf("%s %q %w", name, v, ErrPlaceholder)
		}
	}
	return nil
}

// Check runs Placeholder on each named setting and joins the failures, so
// one startup error lists everything still to fill in.
func Check(getenv func(string) string, names ...string) error {
	var errs []error
	for _, n := range names {
		if err := Placeholder(n, getenv(n)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
