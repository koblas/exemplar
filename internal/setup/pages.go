package setup

import (
	"embed"
	"html/template"
)

// The pages, and every string they show, are in pages/ (.claude/rules/ux-flows.md).
//
//go:embed pages/*.html
var pageFiles embed.FS

var pages = template.Must(template.ParseFS(pageFiles, "pages/*.html"))

// failure names an error page in pages/errors.html.
type failure string

const (
	failSpentState   failure = "fail-spent-state"
	failOtherBrowser failure = "fail-other-browser"
	failConversion   failure = "fail-conversion"
	failKey          failure = "fail-key"
	failStaleForm    failure = "fail-stale-form"
	failNotReady     failure = "fail-not-ready"
	failManifest     failure = "fail-manifest"
	failOtherHost    failure = "fail-other-host"
)
