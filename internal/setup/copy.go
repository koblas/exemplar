package setup

import "html/template"

// Every string the setup pages show (.claude/rules/ux-flows.md). The docs
// (docs/guides/install/quickstart.md#6-register-the-github-app,
// docs/guides/operate/rule-exports.md) quote the step titles and button
// labels: rename them there too.

// Step titles, in order.
const (
	titleCreate  = "Create the App on GitHub"
	titleInstall = "Install it"
	titleFinish  = "Save the Secret and restart exemplar-api"
)

// Button labels.
const (
	labelCreate       = "Create the App on GitHub"
	labelCheckInstall = "Check the installation"
	labelCreateExport = "Create the exports app on GitHub"
	labelBack         = "Back to setup"
)

// failure is an error page: what failed and why, then what to do. NextURL
// is where the button goes; AppsURL, when set, is the GitHub page where an
// App GitHub already created can be deleted.
type failure struct {
	Title, What, Next string
	AppsURL           string
}

// Errors on their own page: the request can't continue the flow in place.
var (
	failSpentState = failure{
		Title: "This link from GitHub was already used or has expired",
		What:  "GitHub's link back to setup works once and lasts an hour.",
		Next: "If setup shows the App as created, carry on there. Otherwise delete the App GitHub created " +
			"in your App settings, then create it again from setup.",
	}
	failOtherBrowser = failure{
		Title: "This browser didn't start that registration",
		What:  "Setup only accepts an App's key in the browser that sent it to GitHub, so it ignored this one.",
		Next:  "Delete the App GitHub just created in your App settings, then create it again from setup in this browser.",
	}
	failConversion = failure{
		Title: "GitHub created the App but didn't hand over its key",
		What:  "Exchanging GitHub's one-time code for the App's id and private key failed. The exemplar-api log has GitHub's answer.",
		Next:  "Delete the App in your App settings, then create it again from setup.",
	}
	failKey = failure{
		Title: "The new App's private key didn't load",
		What:  "GitHub returned a key setup can't parse, so it can't look for the App's installation.",
		Next:  "Delete the App in your App settings, then create it again from setup.",
	}
	failStaleForm = failure{
		Title: "This page is from before exemplar-api restarted",
		What:  "The pod that showed this page is gone, and the new App's key went with it.",
		Next: "Open setup again. If it asks you to create the App, first delete the one GitHub already has " +
			"in your App settings.",
	}
	failNotReady = failure{
		Title: "There's no Secret yet",
		What:  "The Secret holds the App's key and its installation, so it exists once both steps are done.",
		Next: "Create the App and install it from setup first. If you already had, exemplar-api restarted since and " +
			"lost the App's key: delete that App in your App settings, then start again from setup.",
	}
	failManifest = failure{
		Title: "Setup couldn't build the App manifest",
		What:  "This is a bug in Exemplar; the exemplar-api log has the details.",
		Next:  "Report it, with the log, at https://github.com/koblas/exemplar/issues.",
	}
)

// errOtherHost is plain text: the request may come from another site's page.
const errOtherHost = "Setup doesn't answer on %s, against DNS rebinding. Open %s/setup through kubectl port-forward instead.\n"

// Errors shown in place, on the install step.
const (
	errNotInstalled = "GitHub lists no installation of %s on %s. Install it on %s (the account that owns exemplar-config), then check again."
	errListFailed   = "GitHub didn't answer when setup asked for the App's installations. Check again in a moment; if it keeps failing, the exemplar-api log has GitHub's answer."
)

var pages = template.Must(template.New("").Parse(`
{{define "head"}}<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Exemplar setup</title><style>
:root{color-scheme:light dark}
body{font:15px/1.5 system-ui,sans-serif;max-width:46rem;margin:0 auto;padding:0 1rem 3rem}
header{border-bottom:1px solid #8884;padding:.75rem 0;margin-bottom:1rem}button{font:inherit;padding:.35rem .9rem}
code,pre{font-family:ui-monospace,monospace;font-size:13px}pre{white-space:pre-wrap;background:#8881;padding:.5rem;border-radius:4px}
.notice{background:#f5c54222;border:1px solid #f5c54288;padding:.5rem;border-radius:4px}
.error{background:#cf222e1a;border:1px solid #cf222e88;padding:.5rem;border-radius:4px}
.muted{opacity:.7}
ol.steps{padding-left:1.5rem}ol.steps>li{margin:1rem 0}ol.steps h2{font-size:1.05rem;margin:0}
li.done h2::after{content:" \2713";color:#1a7f37}li.todo{opacity:.55}
dl{display:grid;grid-template-columns:max-content 1fr;gap:.2rem 1rem}dt{font-weight:600}dd{margin:0}
</style></head><body><header><strong>Exemplar setup</strong></header>{{end}}

{{define "error"}}{{template "head"}}
<div class="error" role="alert"><h1 style="font-size:1.15rem;margin:0">{{.Title}}</h1>
<p>{{.What}}</p><p>{{.Next}}</p></div>
{{if .AppsURL}}<p>Your App settings: <a href="{{.AppsURL}}">{{.AppsURL}}</a></p>{{end}}
<p><a href="/setup">` + labelBack + `</a></p>
</body></html>{{end}}

{{define "status"}}{{template "head"}}
<h1 style="font-size:1.3rem">Register Exemplar's GitHub App on {{.Org}}</h1>
{{if .OwnerUnknown}}<p class="notice" role="status">GitHub didn't say whether {{.Org}} is an organization or a personal account,
so the GitHub links below assume an organization. If {{.Org}} is your own account, reload this page.</p>{{end}}
{{if .Holding}}<p class="notice" role="status">Until you apply the Secret, the App's key exists only in this exemplar-api pod's memory.
Keep this port-forward running until you've applied the Secret.</p>{{end}}
<ol class="steps">

<li class="{{.StepClass 1}}" {{if eq .Current 1}}aria-current="step"{{end}}><h2>` + titleCreate + `</h2>
{{if eq .Current 1}}
<p>GitHub opens with the App's permissions, events and URLs filled in; you can rename it there. Check these first:</p>
<dl><dt>Webhooks go to</dt><dd><code>{{.URLs.Webhook}}</code></dd><dt>People sign in at</dt><dd><code>{{.URLs.API}}</code></dd></dl>
<p class="muted">If either is wrong, set <code>EXEMPLAR_API_URL</code> (or <code>EXEMPLAR_WEBHOOK_URL</code>) in your overlay, apply it, and open setup again.</p>
<form method="post" action="{{.Review.Action}}"><input type="hidden" name="manifest" value="{{.Review.Manifest}}">
<button type="submit">` + labelCreate + `</button></form>
{{else}}<p>Created <a href="{{.Review.AppURL}}">{{.Review.Name}}</a>.</p>{{end}}
</li>

<li class="{{.StepClass 2}}" {{if eq .Current 2}}aria-current="step"{{end}}><h2>` + titleInstall + `</h2>
{{if eq .Current 2}}
{{if .InstallError}}<p class="error" role="alert">{{.InstallError}}</p>{{end}}
<p><a href="{{.Review.InstallURL}}">Install {{.Review.Name}} on {{.Org}}</a> on GitHub. Choose the repositories Exemplar reviews,
and <code>exemplar-config</code>. Then come back here:</p>
<form method="post" action="/setup/installation"><input type="hidden" name="form" value="{{.Form}}">
<button type="submit">` + labelCheckInstall + `</button></form>
{{else if gt .Current 2}}<p>Installed on {{.Org}} (installation {{.Review.Installation}}).</p>{{end}}
</li>

<li class="{{.StepClass 3}}" {{if eq .Current 3}}aria-current="step"{{end}}><h2>` + titleFinish + `</h2>
{{if eq .Current 3}}
<p>Save <a href="/setup/exemplar-github-app.yaml">exemplar-github-app.yaml</a> and apply it:</p>
<pre>kubectl apply -f exemplar-github-app.yaml</pre>
<details><summary>Show the Secret</summary><pre>{{.Review.Secret}}</pre></details>
<p>For <code>exemplar login</code> from a terminal, tick <b>Enable Device Flow</b> in the <a href="{{.Review.AppURL}}">App's settings</a>.</p>
<details {{if .Exports.Created}}open{{end}}><summary>Rule exports (optional)</summary>
<p>Exports write instruction files to the repositories that opt in, with a second App installed only on those repositories.
Skip this unless you want them; <a href="https://koblas.github.io/exemplar/guides/operate/rule-exports/">you can add it later</a>.</p>
{{if not .Exports.Created}}
<form method="post" action="{{.Exports.Action}}"><input type="hidden" name="manifest" value="{{.Exports.Manifest}}">
<button type="submit">` + labelCreateExport + `</button></form>
{{else}}
<p>Created <a href="{{.Exports.AppURL}}">{{.Exports.Name}}</a>. <a href="{{.Exports.InstallURL}}">Install it</a> only on the repositories
that opt into exports, never on <code>exemplar-config</code>. Save <a href="/setup/exemplar-exports-app.yaml">exemplar-exports-app.yaml</a> and apply it:</p>
<pre>kubectl apply -f exemplar-exports-app.yaml</pre>
{{end}}
</details>
<p>Then restart exemplar-api. It leaves setup mode, so this page goes away; the other workloads have been waiting for the Secret and start on their own:</p>
<pre>kubectl -n {{.Namespace}} rollout restart deployment/exemplar-api</pre>
<p>Once the pods are ready, open <a href="{{.URLs.API}}/ui/setup">{{.URLs.API}}/ui/setup</a> and sign in: it checks the installation and the webhook deliveries.</p>
{{end}}
</li>
</ol>
</body></html>{{end}}
`))
