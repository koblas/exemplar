package envcfg

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var errNotHTTPS = errors.New("must be an https:// URL with a host")

// URLs are the public addresses derived from one setting, EXEMPLAR_API_URL:
// what the GitHub App registers and what people open.
type URLs struct {
	// API is exemplar-api's public base, without a trailing slash.
	API string
	// Webhook is where GitHub posts events: EXEMPLAR_WEBHOOK_URL, or the
	// API base plus /webhook (one host, routed by path).
	Webhook string
	// Callback is the GitHub OAuth redirect URI.
	Callback string
}

// PublicURLs reads EXEMPLAR_API_URL and EXEMPLAR_WEBHOOK_URL.
func PublicURLs(getenv func(string) string) (URLs, error) {
	api, err := httpsBase("EXEMPLAR_API_URL", getenv("EXEMPLAR_API_URL"))
	if err != nil {
		return URLs{}, err
	}
	u := URLs{API: api, Webhook: api + "/webhook", Callback: api + "/oauth/callback"}
	if w := getenv("EXEMPLAR_WEBHOOK_URL"); w != "" {
		if u.Webhook, err = httpsBase("EXEMPLAR_WEBHOOK_URL", w); err != nil {
			return URLs{}, err
		}
		if !strings.HasSuffix(u.Webhook, "/webhook") {
			u.Webhook += "/webhook"
		}
	}
	return u, nil
}

func httpsBase(name, v string) (string, error) {
	p, err := url.Parse(v)
	if err != nil || p.Scheme != "https" || p.Host == "" {
		return "", fmt.Errorf("%s %q %w", name, v, errNotHTTPS)
	}
	return strings.TrimSuffix(v, "/"), nil
}

// Owner returns the owner of an owner/name repo, or "".
func Owner(repo string) string {
	owner, _, ok := strings.Cut(repo, "/")
	if !ok {
		return ""
	}
	return owner
}
