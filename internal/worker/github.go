package worker

import (
	"context"
	"fmt"

	"github.com/google/go-github/v88/github"

	"github.com/koblas/exemplar/internal/gh"
)

// AppClients mints each client from the GitHub App with exactly the
// flavor's permissions, scoped to the one PR repo.
type AppClients struct{ App *gh.App }

// Reader is a FlavorWorker client: contents and pull requests, read only.
func (a AppClients) Reader(ctx context.Context, inst int64, repo string) (*github.Client, error) {
	c, err := a.App.Client(ctx, inst, gh.FlavorWorker, []string{repo})
	if err != nil {
		return nil, fmt.Errorf("mint reader client: %w", err)
	}
	return c, nil
}

// Gate is a FlavorGate client: administration and members, read only.
func (a AppClients) Gate(ctx context.Context, inst int64, repo string) (*github.Client, error) {
	c, err := a.App.Client(ctx, inst, gh.FlavorGate, []string{repo})
	if err != nil {
		return nil, fmt.Errorf("mint gate client: %w", err)
	}
	return c, nil
}

// Poster is a FlavorPoster client, the only one that writes.
func (a AppClients) Poster(ctx context.Context, inst int64, repo string) (*github.Client, error) {
	c, err := a.App.Client(ctx, inst, gh.FlavorPoster, []string{repo})
	if err != nil {
		return nil, fmt.Errorf("mint poster client: %w", err)
	}
	return c, nil
}

// CloneToken is a FlavorWorker installation token for git.
func (a AppClients) CloneToken(ctx context.Context, inst int64, repo string) (string, error) {
	tok, err := a.App.Token(ctx, inst, gh.FlavorWorker, []string{repo})
	if err != nil {
		return "", fmt.Errorf("mint clone token: %w", err)
	}
	return tok.GetToken(), nil
}
