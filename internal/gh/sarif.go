package gh

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/google/go-github/v88/github"
)

// ErrCodeScanningUnavailable is returned when the repo cannot take SARIF
// uploads: code scanning is off, or (private repos) Advanced Security is
// not enabled.
var ErrCodeScanningUnavailable = errors.New("gh: code scanning is not available for this repository")

// UploadSARIF uploads a SARIF 2.1.0 log for the PR's head (spec §6.12),
// gzipped and base64-encoded, on the PR's ref so the alerts show on the
// pull request. It returns the upload id.
func (p *Poster) UploadSARIF(ctx context.Context, pr PR, sarif []byte) (string, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(sarif); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	id, _, err := p.gh.CodeScanning.UploadSarif(ctx, pr.Owner, pr.Repo, &github.SarifAnalysis{
		CommitSHA: new(pr.HeadSHA),
		Ref:       new(fmt.Sprintf("refs/pull/%d/head", pr.Number)),
		Sarif:     new(base64.StdEncoding.EncodeToString(buf.Bytes())),
	})
	var er *github.ErrorResponse
	if errors.As(err, &er) && er.Response != nil && (er.Response.StatusCode == http.StatusForbidden || er.Response.StatusCode == http.StatusNotFound) {
		return "", fmt.Errorf("%w: %s", ErrCodeScanningUnavailable, er.Message)
	}
	if err != nil {
		return "", fmt.Errorf("gh: upload SARIF to %s/%s: %w", pr.Owner, pr.Repo, err)
	}
	return id.GetID(), nil
}

// maxAlertPages bounds CodeScanningAnnotations to 1,000 alerts.
const maxAlertPages = 10

// CodeScanningAnnotations returns a PR's open code-scanning alerts from
// the tools named in tools (case-insensitive), as annotations for linter
// dedupe (spec §6.10): Check is the tool's name, Title the rule id. Code
// scanning analyzes a PR on refs/pull/<n>/merge. A repo without code
// scanning has no alerts (ErrCodeScanningUnavailable is not returned).
func CodeScanningAnnotations(ctx context.Context, c *github.Client, repo string, number int, tools []string) ([]Annotation, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("gh: repo %q must be owner/name", repo)
	}
	want := func(n string) bool {
		return slices.ContainsFunc(tools, func(t string) bool { return strings.EqualFold(t, n) })
	}
	opts := &github.AlertListOptions{State: "open", Ref: fmt.Sprintf("refs/pull/%d/merge", number), ListOptions: github.ListOptions{PerPage: 100}}
	var out []Annotation
	for range maxAlertPages {
		alerts, resp, err := c.CodeScanning.ListAlertsForRepo(ctx, owner, name, opts)
		var er *github.ErrorResponse
		if errors.As(err, &er) && er.Response != nil && (er.Response.StatusCode == http.StatusForbidden || er.Response.StatusCode == http.StatusNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("gh: code scanning alerts of %s#%d: %w", repo, number, err)
		}
		for _, a := range alerts {
			tool := a.GetTool().GetName()
			inst := a.GetMostRecentInstance()
			loc := inst.GetLocation()
			if !want(tool) || loc.GetPath() == "" {
				continue
			}
			out = append(out, Annotation{
				Check: tool, Path: loc.GetPath(), Start: loc.GetStartLine(), End: loc.GetEndLine(),
				Level: a.GetRule().GetSeverity(), Title: a.GetRule().GetID(), Message: inst.GetMessage().GetText(),
			})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.ListOptions.Page = resp.NextPage
	}
	return out, nil
}
