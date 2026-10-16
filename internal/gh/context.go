package gh

import (
	"context"
	"fmt"
	"strings"
)

// ContextFetch mints a context-flavor token for repo (contents and issues
// read; never the config repo) and reads the repo's visibility with it.
// exemplar-search uses the token to fetch its mirror and the visibility
// to keep context within one visibility tier.
func (a *App) ContextFetch(ctx context.Context, installationID int64, repo string) (token, visibility string, err error) {
	tok, err := a.Token(ctx, installationID, FlavorContext, []string{repo})
	if err != nil {
		return "", "", err
	}
	c, err := a.clientWithToken(installationID, tok.GetToken())
	if err != nil {
		return "", "", err
	}
	owner, name, _ := strings.Cut(repo, "/")
	r, _, err := c.Repositories.Get(ctx, owner, name)
	if err != nil {
		return "", "", fmt.Errorf("gh: read %s: %w", repo, err)
	}
	visibility = r.GetVisibility()
	if visibility == "" { // older GHES omits it
		visibility = "public"
		if r.GetPrivate() {
			visibility = "private"
		}
	}
	return tok.GetToken(), visibility, nil
}
