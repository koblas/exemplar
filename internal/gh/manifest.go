package gh

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v88/github"
)

// Registering the App from a manifest (exemplar-api's setup page):
// https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest

// CreatedApp is what GitHub returns when a manifest's code is exchanged:
// everything an installation's exemplar-github-app Secret holds except the
// installation id.
type CreatedApp struct {
	ID            int64
	Slug          string
	HTMLURL       string
	ClientID      string
	ClientSecret  string
	WebhookSecret string
	PEM           string
}

// CompleteManifest exchanges the code GitHub's redirect carries for the new
// App's credentials (POST /app-manifests/{code}/conversions). The call needs
// no authentication, and the code is good for an hour, once.
func CompleteManifest(ctx context.Context, apiBase string, hc *http.Client, code string) (*CreatedApp, error) {
	opts := []github.ClientOptionsFunc{github.WithTimeout(30 * time.Second)}
	if hc != nil && hc.Transport != nil { // go-github rejects a nil transport
		opts = append(opts, github.WithTransport(hc.Transport))
	}
	if apiBase != "" {
		base := strings.TrimSuffix(apiBase, "/") + "/"
		opts = append(opts, github.WithURLs(&base, &base))
	}
	c, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("gh: new client: %w", err)
	}
	cfg, _, err := c.Apps.CompleteAppManifest(ctx, code)
	if err != nil {
		return nil, restErr(err, "complete app manifest")
	}
	return &CreatedApp{
		ID: cfg.GetID(), Slug: cfg.GetSlug(), HTMLURL: cfg.GetHTMLURL(),
		ClientID: cfg.GetClientID(), ClientSecret: cfg.GetClientSecret(),
		WebhookSecret: cfg.GetWebhookSecret(), PEM: cfg.GetPEM(),
	}, nil
}

// OrgInstallation returns the App's installation on the org (or user)
// account, or ErrNotFound when the App isn't installed there yet.
func (a *App) OrgInstallation(ctx context.Context, account string) (int64, error) {
	for inst, err := range a.apps.Apps.ListInstallationsIter(ctx, nil) {
		if err != nil {
			return 0, restErr(err, "list installations")
		}
		if strings.EqualFold(inst.GetAccount().GetLogin(), account) {
			return inst.GetID(), nil
		}
	}
	return 0, fmt.Errorf("%w: installation on %s", ErrNotFound, account)
}
