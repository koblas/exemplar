package playground

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
)

// GitConfigPR reads config PRs from a git checkout of exemplar-config.
type GitConfigPR struct {
	// PR returns the config PR's metadata (gh.ConfigRepo.PullRequest).
	PR func(ctx context.Context, number int) (gh.PullRequest, error)
	// CloneURL is exemplar-config's clone URL.
	CloneURL string
	// Token returns a token that can read exemplar-config ("" for local).
	Token   func(ctx context.Context) (string, error)
	WorkDir string
}

// Load checks the PR out and returns its base and head trees and every
// changed path, both sides of renames included.
func (g *GitConfigPR) Load(ctx context.Context, number int) (fs.FS, fs.FS, []string, string, error) {
	pr, err := g.PR(ctx, number)
	if err != nil {
		return nil, nil, nil, "", err
	}
	token := ""
	if g.Token != nil {
		if token, err = g.Token(ctx); err != nil {
			return nil, nil, nil, "", err
		}
	}
	if err := os.MkdirAll(g.WorkDir, 0o750); err != nil {
		return nil, nil, nil, "", err
	}
	dir, err := os.MkdirTemp(g.WorkDir, fmt.Sprintf("config-pr-%d-", number))
	if err != nil {
		return nil, nil, nil, "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ws, err := engine.Checkout(ctx, filepath.Join(dir, "wt"), engine.CheckoutOptions{
		CloneURL: g.CloneURL, BaseSHA: pr.BaseSHA, HeadSHA: pr.HeadSHA, Token: token, SparseDirs: []string{},
	})
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("playground: checkout config PR %d: %w", number, err)
	}
	b, err := config.GitFS(ctx, ws.Dir, pr.BaseSHA)
	if err != nil {
		return nil, nil, nil, "", err
	}
	h, err := config.GitFS(ctx, ws.Dir, pr.HeadSHA)
	if err != nil {
		return nil, nil, nil, "", err
	}
	diff, err := ws.Diff(ctx)
	if err != nil {
		return nil, nil, nil, "", err
	}
	var changed []string
	for _, f := range diff.Files() {
		changed = append(changed, f.Path)
		if f.OldPath != "" && f.OldPath != f.Path {
			changed = append(changed, f.OldPath)
		}
	}
	return b, h, changed, pr.HeadSHA, nil
}
