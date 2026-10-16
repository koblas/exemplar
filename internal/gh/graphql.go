package gh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/go-github/v88/github"
)

// ErrRateLimited reports that GitHub refused a request for rate or point
// budget reasons. Callers that page (ingest) stop, keep their cursor and
// resume later rather than retrying in a loop.
var ErrRateLimited = errors.New("gh: rate limited")

// ErrNotFound reports a GraphQL NOT_FOUND error (or a REST 404, where a
// caller maps it): the object does not exist or the token cannot see it.
var ErrNotFound = errors.New("gh: not found")

// ErrInvalidRepo is a repository name that is not owner/name.
var ErrInvalidRepo = errors.New("gh: repo must be owner/name")

// ErrNotAFile is a repository path that is a directory, not a file.
var ErrNotAFile = errors.New("gh: not a file")

var (
	// errGraphQL is a GraphQL response carrying errors of no more specific kind.
	errGraphQL       = errors.New("graphql error")
	errCompareStatus = errors.New("unexpected status")
	errCommentRange  = errors.New("gh: invalid comment range")
	errNoConclusion  = errors.New("gh: check conclusion is required")
	errInvalidTeam   = errors.New("gh: team must be @org/slug")
	errNoNodeID      = errors.New("has no node id")
)

// GraphQL runs query against c's GraphQL endpoint and decodes "data" into
// out. GraphQL errors become a Go error; RATE_LIMITED (in the body or as a
// REST rate-limit response) wraps ErrRateLimited.
func GraphQL(ctx context.Context, c *github.Client, query string, vars map[string]any, out any) error {
	req, err := c.NewRequest(ctx, "POST", "graphql", map[string]any{"query": query, "variables": vars})
	if err != nil {
		return fmt.Errorf("gh: graphql request: %w", err)
	}
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := c.Do(req, &resp); err != nil {
		var rl *github.RateLimitError
		var abuse *github.AbuseRateLimitError
		if errors.As(err, &rl) || errors.As(err, &abuse) {
			return fmt.Errorf("%w: %w", ErrRateLimited, err)
		}
		return fmt.Errorf("gh: graphql: %w", err)
	}
	for _, e := range resp.Errors {
		if e.Type == "RATE_LIMITED" {
			return fmt.Errorf("%w: %s", ErrRateLimited, e.Message)
		}
	}
	if len(resp.Errors) > 0 {
		if resp.Errors[0].Type == "NOT_FOUND" {
			return fmt.Errorf("%w: %s", ErrNotFound, resp.Errors[0].Message)
		}
		return fmt.Errorf("gh: %w: %s", errGraphQL, resp.Errors[0].Message)
	}
	if out == nil || len(resp.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("gh: graphql decode: %w", err)
	}
	return nil
}
