package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/search/mirror"
)

// Client calls exemplar-search.
type Client struct {
	base  string
	token string
	hc    *http.Client
}

// NewClient returns a Client for base (e.g. http://exemplar-search:8080).
// hc nil uses a client with a 30 s timeout.
func NewClient(base, token string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("api: bad exemplar-search URL %q", base)
	}
	if token == "" {
		return nil, errors.New("api: token is required")
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{base: strings.TrimSuffix(base, "/"), token: token, hc: hc}, nil
}

// Error is a refused or failed call.
type Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("exemplar-search: %d %s: %s", e.Status, e.Code, e.Msg)
}

// Unwrap maps codes to the mirror package's errors, so callers can test
// errors.Is(err, mirror.ErrNoFile) either side of the API.
func (e *Error) Unwrap() error {
	switch e.Code {
	case CodeBadRequest:
		return mirror.ErrBadRequest
	case CodeForbidden:
		return mirror.ErrForbidden
	case CodeNotMirrored, CodeNotInScope:
		return mirror.ErrNotMirrored
	case CodeNoCommit:
		return mirror.ErrNoCommit
	case CodeNoFile:
		return mirror.ErrNoFile
	}
	return nil
}

func (c *Client) call(ctx context.Context, route string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+route, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("exemplar-search %s: %w", route, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("exemplar-search %s: %w", route, err)
	}
	if resp.StatusCode >= 300 {
		var e ErrorResponse
		_ = json.Unmarshal(data, &e)
		return &Error{Status: resp.StatusCode, Code: e.Code, Msg: e.Error}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("exemplar-search %s: decode: %w", route, err)
	}
	return nil
}

// Fetch relays a default-branch push for repo.
func (c *Client) Fetch(ctx context.Context, repo string) error {
	return c.call(ctx, "/v1/fetch", FetchRequest{Repo: repo}, nil)
}

// Snapshot returns repo's current default-branch commit.
func (c *Client) Snapshot(ctx context.Context, prRepo, repo string) (string, error) {
	out, err := c.SnapshotStatus(ctx, prRepo, repo)
	return out.SnapshotSHA, err
}

// SnapshotStatus returns repo's current commit and whether its search is
// degraded.
func (c *Client) SnapshotStatus(ctx context.Context, prRepo, repo string) (SnapshotResponse, error) {
	var out SnapshotResponse
	err := c.call(ctx, "/v1/snapshot", Target{PRRepo: prRepo, Repo: repo}, &out)
	return out, err
}

// FindReferences runs find_references against repo for a review of prRepo
// pinned to snapshot ("" when not pinned).
func (c *Client) FindReferences(ctx context.Context, prRepo, repo, snapshot, name, definedIn string) (FindReferencesResponse, error) {
	var out FindReferencesResponse
	err := c.call(ctx, "/v1/find_references", FindReferencesRequest{
		PRRepo: prRepo, Repo: repo, Name: name, DefinedIn: definedIn, Snapshot: snapshot,
	}, &out)
	return out, err
}

// Dependents runs dependents against repo for a review of prRepo.
func (c *Client) Dependents(ctx context.Context, prRepo, repo, snapshot, path string, depth int) (DependentsResponse, error) {
	var out DependentsResponse
	err := c.call(ctx, "/v1/dependents", DependentsRequest{
		PRRepo: prRepo, Repo: repo, Path: path, Depth: depth, Snapshot: snapshot,
	}, &out)
	return out, err
}

// ReadFile returns lines start..end of path at commit.
func (c *Client) ReadFile(ctx context.Context, prRepo, repo, commit, path string, start, end int) (ReadFileResponse, error) {
	var out ReadFileResponse
	err := c.call(ctx, "/v1/read_file", ReadFileRequest{PRRepo: prRepo, Repo: repo, Commit: commit, Path: path, Start: start, End: end}, &out)
	return out, err
}

// Log returns up to n commits touching path as of commit.
func (c *Client) Log(ctx context.Context, prRepo, repo, commit, path string, n int) ([]mirror.Commit, error) {
	var out LogResponse
	err := c.call(ctx, "/v1/log", LogRequest{PRRepo: prRepo, Repo: repo, Commit: commit, Path: path, N: n}, &out)
	return out.Commits, err
}

// SearchText runs search_text against repo for a review of prRepo pinned
// to snapshot ("" when not pinned).
func (c *Client) SearchText(ctx context.Context, prRepo, repo, snapshot, pattern, pathFilter, lang string) (SearchTextResponse, error) {
	var out SearchTextResponse
	err := c.call(ctx, "/v1/search_text", SearchTextRequest{
		PRRepo: prRepo, Repo: repo, Pattern: pattern, PathFilter: pathFilter, Lang: lang, Snapshot: snapshot,
	}, &out)
	return out, err
}

// FindDefinition runs find_definition against repo for a review of prRepo
// pinned to snapshot ("" when not pinned).
func (c *Client) FindDefinition(ctx context.Context, prRepo, repo, snapshot, name, nearPath string) (FindDefinitionResponse, error) {
	var out FindDefinitionResponse
	err := c.call(ctx, "/v1/find_definition", FindDefinitionRequest{
		PRRepo: prRepo, Repo: repo, Name: name, NearPath: nearPath, Snapshot: snapshot,
	}, &out)
	return out, err
}
