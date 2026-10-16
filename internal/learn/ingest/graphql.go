package ingest

import "time"

// prQuery pages a repo's closed and merged PRs. reviewThreads.comments
// (first: 1) is the thread root. Thread and file lists are capped
// at 100 per PR; PRs beyond that are counted as truncated.
const prQuery = `query($owner: String!, $name: String!, $first: Int!, $after: String) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    pullRequests(states: [MERGED, CLOSED], orderBy: {field: UPDATED_AT, direction: ASC}, first: $first, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number
        author { __typename login }
        headRefOid
        baseRefOid
        mergedAt
        files(first: 100) { pageInfo { hasNextPage endCursor } nodes { path } }
        reviews(first: 50, states: APPROVED) { nodes { author { __typename login } } }
        reviewThreads(first: 100) {
          pageInfo { hasNextPage endCursor }
          nodes {
            isResolved
            comments(first: 1) {
              nodes {
                databaseId
                fullDatabaseId
                url
                body
                path
                diffHunk
                createdAt
                author { __typename login }
                originalCommit { oid }
                originalLine
                originalStartLine
              }
            }
          }
        }
      }
    }
  }
}`

// filesQuery reads the default branch's exclusion and ownership files.
const filesQuery = `query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    gitattributes: object(expression: "HEAD:.gitattributes") { ... on Blob { text } }
    exemplarignore: object(expression: "HEAD:.exemplarignore") { ... on Blob { text } }
    codeownersGithub: object(expression: "HEAD:.github/CODEOWNERS") { ... on Blob { text } }
    codeownersRoot: object(expression: "HEAD:CODEOWNERS") { ... on Blob { text } }
    codeownersDocs: object(expression: "HEAD:docs/CODEOWNERS") { ... on Blob { text } }
  }
}`

const teamQuery = `query($org: String!, $slug: String!, $after: String) {
  organization(login: $org) {
    team(slug: $slug) {
      members(first: 100, after: $after) { nodes { login } pageInfo { hasNextPage endCursor } }
    }
  }
}`

type pageInfo struct {
	HasNextPage bool    `json:"hasNextPage"`
	EndCursor   *string `json:"endCursor"`
}

type prPage struct {
	RateLimit struct {
		Cost      int        `json:"cost"`
		Remaining int        `json:"remaining"`
		ResetAt   *time.Time `json:"resetAt"`
	} `json:"rateLimit"`
	Repository struct {
		PullRequests struct {
			PageInfo pageInfo      `json:"pageInfo"`
			Nodes    []pullRequest `json:"nodes"`
		} `json:"pullRequests"`
	} `json:"repository"`
}

type pullRequest struct {
	Number     int        `json:"number"`
	Author     Author     `json:"author"`
	HeadRefOid string     `json:"headRefOid"`
	BaseRefOid string     `json:"baseRefOid"`
	MergedAt   *time.Time `json:"mergedAt"`
	Files      struct {
		PageInfo pageInfo `json:"pageInfo"`
		Nodes    []struct {
			Path string `json:"path"`
		} `json:"nodes"`
	} `json:"files"`
	Reviews struct {
		Nodes []struct {
			Author Author `json:"author"`
		} `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads struct {
		PageInfo pageInfo `json:"pageInfo"`
		Nodes    []struct {
			IsResolved bool `json:"isResolved"`
			Comments   struct {
				Nodes []reviewComment `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
}

type reviewComment struct {
	DatabaseID     int64     `json:"databaseId"`
	FullDatabaseID string    `json:"fullDatabaseId"`
	URL            string    `json:"url"`
	Body           string    `json:"body"`
	Path           string    `json:"path"`
	DiffHunk       string    `json:"diffHunk"`
	CreatedAt      time.Time `json:"createdAt"`
	Author         Author    `json:"author"`
	OriginalCommit *struct {
		Oid string `json:"oid"`
	} `json:"originalCommit"`
	OriginalLine      *int `json:"originalLine"`
	OriginalStartLine *int `json:"originalStartLine"`
}

type blob struct {
	Text string `json:"text"`
}

type repoFiles struct {
	Gitattributes    blob `json:"gitattributes"`
	Exemplarignore   blob `json:"exemplarignore"`
	CodeownersGithub blob `json:"codeownersGithub"`
	CodeownersRoot   blob `json:"codeownersRoot"`
	CodeownersDocs   blob `json:"codeownersDocs"`
}

// codeowners returns the file GitHub would use: .github/, then root, then
// docs/ (codeowners.Locations).
func (f repoFiles) codeowners() string {
	for _, t := range []string{f.CodeownersGithub.Text, f.CodeownersRoot.Text, f.CodeownersDocs.Text} {
		if t != "" {
			return t
		}
	}
	return ""
}
