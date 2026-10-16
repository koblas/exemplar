package gh

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/v88/github"
)

// Webhook reliability: GitHub does not retry failed deliveries,
// so a reconciler requests redelivery and a sweeper finds PRs whose events
// never arrived.

// Delivery is one attempt to deliver a webhook. Redeliveries of an event
// share its GUID (the X-GitHub-Delivery header).
type Delivery struct {
	ID          int64
	GUID        string
	StatusCode  int
	Redelivery  bool
	DeliveredAt time.Time
	Event       string
}

// maxDeliveryPages bounds one listing (100 attempts a page).
const maxDeliveryPages = 50

// HookDeliveries lists the App's delivery attempts since the given time,
// newest first, as the App (JWT).
func (a *App) HookDeliveries(ctx context.Context, since time.Time) ([]Delivery, error) {
	var out []Delivery
	opts := &github.ListCursorOptions{PerPage: 100}
	for range maxDeliveryPages {
		page, resp, err := a.apps.Apps.ListHookDeliveries(ctx, opts)
		if err != nil {
			return out, restErr(err, "app hook deliveries")
		}
		for _, d := range page {
			at := d.GetDeliveredAt().Time
			if at.Before(since) {
				return out, nil // newest first: the rest are older
			}
			out = append(out, Delivery{
				ID: d.GetID(), GUID: d.GetGUID(), StatusCode: d.GetStatusCode(),
				Redelivery: d.GetRedelivery(), DeliveredAt: at, Event: d.GetEvent(),
			})
		}
		if resp == nil || resp.Cursor == "" {
			return out, nil
		}
		opts.Cursor = resp.Cursor
	}
	return out, nil
}

// Redeliver asks GitHub to deliver an attempt's event again.
func (a *App) Redeliver(ctx context.Context, deliveryID int64) error {
	_, _, err := a.apps.Apps.RedeliverHookDelivery(ctx, deliveryID)
	// GitHub answers 202 Accepted, which go-github reports as an error.
	var accepted *github.AcceptedError
	if err != nil && !errors.As(err, &accepted) {
		return fmt.Errorf("gh: redeliver %d: %w", deliveryID, err)
	}
	return nil
}

// RepoInstallation returns the App's installation id on repo.
func (a *App) RepoInstallation(ctx context.Context, repo string) (int64, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return 0, fmt.Errorf("gh: repo %q must be owner/name", repo)
	}
	inst, _, err := a.apps.Apps.GetRepositoryInstallation(ctx, owner, name)
	if err != nil {
		return 0, restErr(err, "installation on "+repo)
	}
	return inst.GetID(), nil
}

// OpenPR is one open pull request the sweeper found.
type OpenPR struct {
	Repo    string
	Number  int
	HeadSHA string
	Draft   bool
	// HeadRepo is empty when the head repository was deleted.
	HeadRepo string
}

const openPRsQuery = `query($q: String!, $after: String) {
  search(type: ISSUE, query: $q, first: 100, after: $after) {
    nodes { ... on PullRequest { number headRefOid isDraft repository { nameWithOwner } headRepository { nameWithOwner } } }
    pageInfo { hasNextPage endCursor }
  }
}`

// maxSearchPages: GitHub search returns at most 1,000 results.
const maxSearchPages = 10

// SearchOpenPRs runs one GraphQL search for open PRs in repos (one
// installation's) updated since the given time, with a sweep token.
func (a *App) SearchOpenPRs(ctx context.Context, installationID int64, repos []string, since time.Time) ([]OpenPR, error) {
	c, err := a.Client(ctx, installationID, FlavorSweep, repos)
	if err != nil {
		return nil, err
	}
	return SearchOpenPRs(ctx, c, repos, since)
}

// SearchOpenPRs runs the sweep search with c.
func SearchOpenPRs(ctx context.Context, c *github.Client, repos []string, since time.Time) ([]OpenPR, error) {
	q := "is:pr is:open updated:>=" + since.UTC().Format("2006-01-02T15:04:05Z")
	var qSb116 strings.Builder
	for _, r := range repos {
		qSb116.WriteString(" repo:" + r)
	}
	q += qSb116.String()
	var out []OpenPR
	vars := map[string]any{"q": q, "after": nil}
	for range maxSearchPages {
		var res struct {
			Search struct {
				Nodes []struct {
					Number     int    `json:"number"`
					HeadRefOid string `json:"headRefOid"`
					IsDraft    bool   `json:"isDraft"`
					Repository struct {
						NameWithOwner string `json:"nameWithOwner"`
					} `json:"repository"`
					HeadRepository *struct {
						NameWithOwner string `json:"nameWithOwner"`
					} `json:"headRepository"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"search"`
		}
		if err := GraphQL(ctx, c, openPRsQuery, vars, &res); err != nil {
			return out, err
		}
		for _, n := range res.Search.Nodes {
			if n.Number == 0 {
				continue // not a pull request
			}
			pr := OpenPR{Repo: n.Repository.NameWithOwner, Number: n.Number, HeadSHA: n.HeadRefOid, Draft: n.IsDraft}
			if n.HeadRepository != nil {
				pr.HeadRepo = n.HeadRepository.NameWithOwner
			}
			out = append(out, pr)
		}
		if !res.Search.PageInfo.HasNextPage {
			return out, nil
		}
		vars["after"] = res.Search.PageInfo.EndCursor
	}
	return out, nil
}
