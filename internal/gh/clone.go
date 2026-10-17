package gh

import "strings"

// DefaultWebURL is github.com's web base; GitHub Enterprise Server's is
// https://<host>/.
const DefaultWebURL = "https://github.com/"

// WebBase is web with a trailing slash, or DefaultWebURL when web is empty.
func WebBase(web string) string {
	if web == "" {
		return DefaultWebURL
	}
	if !strings.HasSuffix(web, "/") {
		web += "/"
	}
	return web
}

// CloneURL returns a repo's HTTPS clone URL under the web base web
// (EXEMPLAR_GH_WEB_URL; github.com when empty).
func CloneURL(web string) func(repo string) string {
	base := WebBase(web)
	return func(repo string) string { return base + repo + ".git" }
}
