// Package ghuser checks GitHub user tokens for exemplar-api: who the
// token's user is, and whether they can read a repo (spec §13 IN-2:
// GitHub user OAuth for repo-level authorization, cached for 5 minutes).
// Tokens are hashed for the cache keys and never stored or logged.
package ghuser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TTL is how long a token's login and repo access are trusted.
const TTL = 5 * time.Minute

// maxEntries bounds each cache before expired entries are swept.
const maxEntries = 10000

// Checker answers for tokens against one GitHub API.
type Checker struct {
	// API is https://api.github.com/ (GHES: https://<host>/api/v3/).
	API  string
	HTTP *http.Client
	Now  func() time.Time

	mu     sync.Mutex
	logins map[string]entry // token hash -> login
	access map[string]entry // token hash + repo -> readable ("1") or not ("0")
}

type entry struct {
	value string
	until time.Time
}

// New returns a Checker; api "" is github.com.
func New(api string, c *http.Client) *Checker {
	if api == "" {
		api = "https://api.github.com/"
	}
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	return &Checker{API: api, HTTP: c, Now: time.Now, logins: map[string]entry{}, access: map[string]entry{}}
}

func key(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (c *Checker) get(m map[string]entry, k string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := m[k]
	if !ok || c.Now().After(e.until) {
		return "", false
	}
	return e.value, true
}

func (c *Checker) put(m map[string]entry, k, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	if len(m) > maxEntries {
		for k, e := range m {
			if now.After(e.until) {
				delete(m, k)
			}
		}
	}
	m[k] = entry{value: v, until: now.Add(TTL)}
}

// ErrRefused is a token GitHub does not accept, or not a user's.
var ErrRefused = errors.New("ghuser: token refused")

// Login is the token's GitHub user, which must be a user account.
func (c *Checker) Login(ctx context.Context, token string) (string, error) {
	k := key(token)
	if l, ok := c.get(c.logins, k); ok {
		return l, nil
	}
	var u struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	}
	code, err := c.call(ctx, token, "user", &u)
	switch {
	case err != nil:
		return "", err
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return "", ErrRefused
	case code != http.StatusOK:
		return "", fmt.Errorf("ghuser: GET /user: %d", code)
	case u.Login == "" || !strings.EqualFold(u.Type, "User"):
		return "", ErrRefused
	}
	c.put(c.logins, k, u.Login)
	return u.Login, nil
}

// CanRead reports whether the token's user can read repo (owner/name).
func (c *Checker) CanRead(ctx context.Context, token, repo string) (bool, error) {
	if strings.Count(repo, "/") != 1 || strings.ContainsAny(repo, "?#% ") {
		return false, nil
	}
	k := key(token) + " " + strings.ToLower(repo)
	if v, ok := c.get(c.access, k); ok {
		return v == "1", nil
	}
	var out struct {
		Permissions struct {
			Pull bool `json:"pull"`
		} `json:"permissions"`
	}
	code, err := c.call(ctx, token, "repos/"+repo, &out)
	var ok bool
	switch {
	case err != nil:
		return false, err
	case code == http.StatusNotFound || code == http.StatusForbidden:
	case code != http.StatusOK:
		return false, fmt.Errorf("ghuser: GET /repos/%s: %d", repo, code)
	default:
		ok = out.Permissions.Pull
	}
	v := "0"
	if ok {
		v = "1"
	}
	c.put(c.access, k, v)
	return ok, nil
}

// call GETs path with the user's token. A non-200 status is returned, not
// an error, so callers can tell refusal from failure.
func (c *Checker) call(ctx context.Context, token, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.API+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK || out == nil {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, json.Unmarshal(body, out)
}

// InOrgs reports whether login is a member of any of orgs, asked with
// their own token (a member sees their own membership, public or not).
func (c *Checker) InOrgs(ctx context.Context, token, login string, orgs []string) (bool, error) {
	k := key(token) + " orgs " + strings.ToLower(strings.Join(orgs, ","))
	if v, ok := c.get(c.access, k); ok {
		return v == "1", nil
	}
	in := false
	for _, org := range orgs {
		org = strings.TrimSpace(org)
		if org == "" || strings.ContainsAny(org, "/?#% ") {
			continue
		}
		code, err := c.call(ctx, token, "orgs/"+org+"/members/"+login, nil)
		if err != nil {
			return false, err
		}
		switch code {
		case http.StatusNoContent:
			in = true
		case http.StatusNotFound, http.StatusFound, http.StatusForbidden:
		default:
			return false, fmt.Errorf("ghuser: GET /orgs/%s/members: %d", org, code)
		}
		if in {
			break
		}
	}
	v := "0"
	if in {
		v = "1"
	}
	c.put(c.access, k, v)
	return in, nil
}
