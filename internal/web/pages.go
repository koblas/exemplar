package web

import (
	"fmt"
	"html/template"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

var funcs = template.FuncMap{
	"day": func(t pgtype.Timestamptz) string {
		if !t.Valid {
			return "—"
		}
		return t.Time.UTC().Format(time.DateOnly)
	},
	"when": func(t pgtype.Timestamptz) string {
		if !t.Valid {
			return "—"
		}
		return t.Time.UTC().Format("2006-01-02 15:04 UTC")
	},
	"usd":   func(f float64) string { return fmt.Sprintf("$%.2f", f) },
	"ratio": func(f float64) string { return fmt.Sprintf("%.0f%%", 100*f) },
	"short": func(s string) string {
		if len(s) > 12 {
			return s[:12]
		}
		return s
	},
}

var pages = template.Must(template.New("").Funcs(funcs).Parse(`
{{define "head"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>{{.Title}} · Exemplar</title>
<link rel="stylesheet" href="/static/bulma-1.0.4.min.css"><link rel="stylesheet" href="/static/app.css">
</head><body>
<header class="section py-3"><div class="container is-max-widescreen">
<div class="is-flex is-align-items-center is-justify-content-space-between mb-2"><strong>Exemplar</strong>
<form class="is-flex is-align-items-center" method="post" action="/ui/signout"><span class="muted mr-2">{{.Login}}</span> <button class="button is-small"
type="submit">Sign out</button></form></div>
<nav class="tabs"><ul>
<li {{if eq .Nav "rules"}}class="is-active"{{end}}><a href="/ui/" {{if eq .Nav "rules"}}aria-current="page"{{end}}>Rules</a></li>
<li {{if eq .Nav "candidates"}}class="is-active"{{end}}><a href="/ui/candidates" {{if eq .Nav "candidates"}}aria-current="page"{{end}}>Candidates</a></li>
<li {{if eq .Nav "audit"}}class="is-active"{{end}}><a href="/ui/audit" {{if eq .Nav "audit"}}aria-current="page"{{end}}>Auto-approve audit</a></li>
<li {{if eq .Nav "cost"}}class="is-active"{{end}}><a href="/ui/cost" {{if eq .Nav "cost"}}aria-current="page"{{end}}>Cost</a></li>
<li {{if eq .Nav "playground"}}class="is-active"{{end}}><a href="/ui/playground" {{if eq .Nav "playground"}}aria-current="page"{{end}}>Playground</a></li>
<li {{if eq .Nav "actions"}}class="is-active"{{end}}><a href="/ui/actions" {{if eq .Nav "actions"}}aria-current="page"{{end}}>Your actions</a></li>
<li {{if eq .Nav "setup"}}class="is-active"{{end}}><a href="/ui/setup" {{if eq .Nav "setup"}}aria-current="page"{{end}}>Setup</a></li>
</ul></nav></div></header>
<main class="section pt-4"><div class="container is-max-widescreen content">
{{if .Notice}}<article class="message is-warning"><p class="message-body">{{.Notice}}</p></article>{{end}}
<h1 class="title is-4">{{.Title}}</h1>
{{end}}
{{define "foot"}}</div></main></body></html>{{end}}

{{define "rules"}}{{template "head" .}}{{with .Body}}
<form class="field is-grouped is-grouped-multiline is-align-items-flex-end" method="get" action="/ui/">
<div class="control"><label class="label" for="owner">Owner</label><span class="select"><select id="owner" name="owner"><option value="">any</option>{{range .Owners}}<option
{{if eq . $.Body.Owner}}selected{{end}}>{{.}}</option>{{end}}</select></span></div>
<div class="control"><label class="label" for="status">Status</label><span class="select"><select id="status" name="status"><option value="">any</option>{{range .Statuses}}<option
{{if eq . $.Body.Status}}selected{{end}}>{{.}}</option>{{end}}</select></span></div>
<div class="control"><label class="label">Search <input class="input" name="q" value="{{.Search}}"></label></div> <div class="control"><button class="button is-primary"
type="submit">Filter</button></div>
</form>
<p class="muted">Outcomes over the last {{.Window}} days. Effective FP is weighted (spec §9.2).</p>
<div class="table-container"><table class="table is-fullwidth
is-hoverable"><thead><tr><th>Rule</th><th>Status</th><th>Severity</th><th>Category</th><th>Owner</th><th>Posted</th><th>Acceptance</th><th>Effective FP</th><th></th></tr></thead><tbody>
{{range .Rules}}<tr><td><a href="/ui/rules/{{.RuleID}}"><code>{{.RuleID}}</code></a>{{if ne .Scope "global"}} <span class="muted">({{.Scope}})</span>{{end}}{{if .Exported}} ` +
	`<span class="muted">exported</span>{{end}}</td>
<td>{{.Status}}</td><td>{{.Severity}}</td><td>{{.Category}}</td><td>{{.Owner}}</td><td class="has-text-right">{{.Posted}}</td><td class="has-text-right">{{.Acceptance}}</td><td
class="has-text-right">{{.EFP}}</td><td>{{.Health}}</td></tr>
{{else}}<tr><td colspan="9" class="muted">No rules match.</td></tr>{{end}}
</tbody></table></div>
{{end}}{{template "foot"}}{{end}}

{{define "rule"}}{{template "head" .}}{{with .Body}}
<p>{{.Rule.Status}} · {{.Rule.Severity}} · {{.Rule.Category}} · {{.Rule.Kind}} · owner {{.Rule.Owner}}{{if .Rule.Exported}} · exported to coding agents{{end}}</p>
<p>Last 28 days: {{.Rule.Posted}} posted, acceptance {{.Rule.Acceptance}}, effective FP {{.Rule.EFP}}.</p>
{{if eq .Rule.Scope "global"}}<p><a href="/ui/playground?rule={{.Rule.RuleID}}">Try a change to this rule in the playground</a></p>{{end}}
{{if and .Actions (ne .Rule.Status "killed")}}<details><summary>Owner actions</summary>
<p class="muted">Each opens an exemplar-config PR in your name and merges it where branch protection allows. Only the rule's owners and the platform team can.</p>
{{if eq .Rule.Status "active"}}<form method="post" action="/ui/actions"><input type="hidden" name="action" value="shadow_rule">` +
	`<input type="hidden" name="target" value="{{.Rule.RuleID}}"><input type="hidden" name="t" value="{{.ShadowToken}}">
<div class="field is-grouped is-align-items-flex-end"><div class="control is-expanded"><label class="label">Why (optional) <input class="input" name="reason" maxlength="1000"></label></div> ` +
	`<div class="control"><button class="button is-warning" type="submit">Move to shadow</button></div></div></form>{{end}}
{{if .Shipped}}<p class="muted">This rule ships with Exemplar. To turn it off, add it to <code>disabled_rules</code> in <code>global.yaml</code>.</p>{{else}}` +
	`<form method="post" action="/ui/actions"><input type="hidden" name="action" value="kill_rule"><input type="hidden" name="target" value="{{.Rule.RuleID}}">` +
	`<input type="hidden" name="t" value="{{.KillToken}}">
<div class="field is-grouped is-align-items-flex-end"><div class="control is-expanded"><label class="label">Why <input class="input" name="reason" maxlength="1000" required></label></div> ` +
	`<div class="control"><button class="button is-danger" type="submit">Kill</button></div></div></form>{{end}}
</details>{{end}}
<h2 class="title is-5">By week</h2>
<div class="table-container"><table class="table is-fullwidth
is-hoverable"><thead><tr><th>Week</th><th>Posted</th><th>Accepted</th><th>Rejected</th><th>Ignored</th><th>Acceptance</th></tr></thead><tbody>
{{range .Weeks}}<tr><td>{{day .Week}}</td><td class="has-text-right">{{.Posted}}</td><td class="has-text-right">{{.Accepted}}</td><td class="has-text-right">{{.Rejected}}</td><td
class="has-text-right">{{.Ignored}}</td>` +
	`<td class="has-text-right">{{ratio .Acceptance}}</td></tr>
{{else}}<tr><td colspan="6" class="muted">No weeks yet.</td></tr>{{end}}
</tbody></table></div>
<h2 class="title is-5">Recent findings</h2>
<div class="table-container"><table class="table is-fullwidth is-hoverable"><thead><tr><th>Finding</th><th>PR</th><th>Where</th><th>State</th><th>Outcome</th><th>Posted</th></tr></thead><tbody>
{{range .Findings}}<tr><td><a href="/ui/findings/{{.ID}}">{{.ID}}</a></td><td><a href="{{.PR}}">{{.Repo}}#{{.PrNumber}}</a></td><td><code>{{.Path}}:{{.StartLine}}</code></td>` +
	`<td>{{.State}}</td><td>{{.Outcome}}</td><td>{{day .CreatedAt}}</td></tr>
{{else}}<tr><td colspan="6" class="muted">None you can see.</td></tr>{{end}}
</tbody></table></div>
{{if .Hidden}}<p class="muted">{{.Hidden}} more in repos you cannot read.</p>{{end}}
{{end}}{{template "foot"}}{{end}}

{{define "finding"}}{{template "head" .}}{{with .Body}}
<p>Rule <a href="/ui/rules/{{.F.RuleID}}"><code>{{.F.RuleID}}</code></a> ({{.F.Lane}}, {{.F.Severity}}) on <a href="{{.PR}}">{{.F.Repo}}#{{.F.PrNumber}}</a>,
<code>{{.F.Path}}:{{.F.StartLine}}{{if ne .F.StartLine .F.EndLine}}-{{.F.EndLine}}{{end}}</code> ({{.F.Side}}).
{{if .Comment}}<a href="{{.Comment}}">The comment on GitHub</a>.{{end}}</p>
<p>State {{.F.State}}{{if .F.SuppressedReason}} ({{.F.SuppressedReason}}){{end}}{{if .F.VerifyVerdict}} · verifier {{.F.VerifyVerdict}}{{end}} · {{when .F.CreatedAt}}</p>
{{if .Recorded}}<article class="message is-warning"><p class="message-body">Recorded as a false positive. Rule owners see it in their digest.</p></article>{{end}}
<h2 class="title is-5">Explanation</h2><pre>{{.F.Message}}</pre>
{{if .F.SuggestedFix}}<h2 class="title is-5">Suggested fix{{if not .F.FixValid}} <span class="muted">(not validated; not posted)</span>{{end}}</h2><pre>{{.F.SuggestedFix}}</pre>{{end}}
<h2 class="title is-5">Feedback</h2>
<div class="table-container"><table class="table is-fullwidth
is-hoverable"><thead><tr><th>When</th><th>Signal</th><th>Who</th><th>Role</th><th>Weight</th><th>Via</th><th>Said</th></tr></thead><tbody>
{{range .Events}}<tr><td>{{when .ObservedAt}}</td><td>{{.Kind}}</td><td>{{.Actor}}</td><td>{{.Role}}</td><td class="has-text-right">{{.Weight}}</td><td>{{.Via}}</td><td>{{.ReasonText}}</td></tr>
{{else}}<tr><td colspan="7" class="muted">No feedback yet.</td></tr>{{end}}
</tbody></table></div>
<form method="post" action="/ui/findings/{{.F.ID}}/false-positive"><input type="hidden" name="t" value="{{.Token}}">
<div class="field is-grouped is-align-items-flex-end"><div class="control is-expanded"><label class="label">Why it is wrong (optional) <input class="input" name="text"
maxlength="1000"></label></div> ` +
	`<div class="control"><button class="button" type="submit">Mark false positive</button></div></div></form>
{{end}}{{template "foot"}}{{end}}

{{define "candidates"}}{{template "head" .}}
<p class="muted">Open candidates: rules owners wrote by hand (owner seeds) first, then mined ones by score, with the repos you can read among those their source comments came from.</p>
<div class="table-container"><table class="table is-fullwidth
is-hoverable"><thead><tr><th>Score</th><th>Candidate</th><th>Cluster</th><th>Reviewers</th><th>Owner</th><th>Status</th><th>Repos</th><th></th></tr></thead><tbody>
{{range .Body}}<tr><td class="has-text-right">{{printf "%.2f" .Score}}</td><td>{{if .Hidden}}<span class="muted">Candidate {{.ID}}: text hidden, ` +
	`some source comments are in repos you cannot read.</span>{{else}}<strong>{{.Title}}</strong><br><span class="muted">{{.Description}}</span>{{end}}` +
	`{{if .ConfigPrUrl}}<br>` +
	`<a href="{{.ConfigPrUrl}}">Config PR</a>{{end}}</td>
<td class="has-text-right">{{.ClusterSize}}</td><td class="has-text-right">{{.DistinctReviewers}}</td><td>{{.Owner}}</td><td>{{.Status}}</td>` +
	`<td>{{if eq .SourceKind "owner_seed"}}owner seed{{if .SeedAuthor}} by @{{.SeedAuthor}}{{end}}` +
	`{{else}}{{range $i, $r := .Visible}}{{if $i}}, {{end}}{{$r}}{{end}}{{end}}</td>
<td><form method="post" action="/ui/actions"><input type="hidden" name="action" value="kill_candidate"><input type="hidden" name="target" value="{{.ID}}">` +
	`<input type="hidden" name="t" value="{{.Token}}">
<div class="field has-addons"><div class="control"><input class="input is-small" name="reason" maxlength="1000" required placeholder="why"></div> ` +
	`<div class="control"><button class="button is-small is-danger" type="submit">Kill</button></div></div></form></td></tr>
{{else}}<tr><td colspan="8" class="muted">No open candidates you can see.</td></tr>{{end}}
</tbody></table></div>
{{template "foot"}}{{end}}

{{define "audit"}}{{template "head" .}}{{with .Body}}
<p class="muted">The latest 500 production decisions, in repos you can read. Each links to its PR.</p>
{{if .Repos}}<details><summary>Kill switches</summary>
<p class="muted">Turns auto-approve off for a repo through an exemplar-config PR in your name, merged where branch protection allows. The platform team and the repo's ` +
	`approvable owners can. Turning it back on is a reviewed config PR.</p>
<div class="table-container"><table class="table is-fullwidth is-hoverable"><tbody>{{range .Repos}}<tr><td>{{.Repo}}</td><td><form method="post" action="/ui/actions"><input type="hidden"
name="action" value="kill_switch">` +
	`<input type="hidden" name="target" value="{{.Repo}}"><input type="hidden" name="t" value="{{.Token}}">
<div class="field has-addons"><div class="control"><input class="input is-small" name="reason" maxlength="1000" placeholder="why (optional)"></div> ` +
	`<div class="control"><button class="button is-small is-danger" type="submit">Turn auto-approve off</button></div></div></form></td></tr>{{end}}</tbody></table></div>
</details>{{end}}
<div class="table-container"><table class="table is-fullwidth
is-hoverable"><thead><tr><th>When</th><th>PR</th><th>Head</th><th>Decision</th><th>Stage 1 reasons</th><th>Stage 2</th></tr></thead><tbody>
{{range .Decisions}}<tr><td>{{when .At}}</td><td><a href="{{.PR}}">{{.Repo}}#{{.PrNumber}}</a></td><td><code>{{short .HeadSha}}</code></td><td>{{.Decision}}</td>` +
	`<td>{{.Stage1Reasons}}</td><td>{{.Stage2}}</td></tr>
{{else}}<tr><td colspan="6" class="muted">No decisions you can see.</td></tr>{{end}}
</tbody></table></div>
{{end}}{{template "foot"}}{{end}}

{{define "setup"}}{{template "head" .}}
<p class="muted">What the installation needs from GitHub, checked now. As JSON: <a href="/ui/setup.json">/ui/setup.json</a>.</p>
<div class="table-container"><table class="table is-fullwidth is-hoverable"><thead><tr><th>Check</th><th>Status</th><th>Found</th><th>To fix</th></tr></thead><tbody>
{{range .Body}}<tr><td>{{.Name}}</td><td>{{.Status}}</td><td>{{.Detail}}</td><td>{{.Fix}}</td></tr>
{{else}}<tr><td colspan="4" class="muted">No checks configured.</td></tr>{{end}}
</tbody></table></div>
{{template "foot"}}{{end}}

{{define "actions"}}{{template "head" .}}
<p class="muted">Your last 50 owner actions. Each runs in a worker job, which checks you may take it; reload to see the outcome.</p>
<div class="table-container"><table class="table is-fullwidth
is-hoverable"><thead><tr><th>Requested</th><th>Action</th><th>Target</th><th>Reason</th><th>Status</th><th>Result</th><th>PR</th></tr></thead><tbody>
{{range .Body}}<tr><td>{{when .CreatedAt}}</td><td>{{.Action}}</td><td><code>{{.Target}}</code></td><td>{{.Reason}}</td><td>{{.Status}}</td>` +
	`<td>{{if .Result}}{{.Result}}{{end}}</td><td>{{if .PrUrl}}<a href="{{.PrUrl}}">PR</a>{{end}}</td></tr>
{{else}}<tr><td colspan="7" class="muted">No actions yet.</td></tr>{{end}}
</tbody></table></div>
{{template "foot"}}{{end}}

{{define "cost"}}{{template "head" .}}{{with .Body}}
<form class="field is-grouped is-align-items-flex-end" method="get" action="/ui/cost"><div class="control"><label class="label">Days ` +
	`<input class="input" name="days" type="number" min="1" max="365" value="{{.Days}}"></label></div> <div class="control"><button class="button is-primary" type="submit">Show</button></div></form>
<p>{{usd .Total}} over the last {{.Days}} days. Cost is informational (PRD §13.2).</p>
<h2 class="title is-5">By caller</h2>
<div class="table-container"><table class="table is-fullwidth is-hoverable"><thead><tr><th>Caller</th><th>Calls</th><th>Spend</th></tr></thead><tbody>
{{range .ByCaller}}<tr><td>{{.Key}}</td><td class="has-text-right">{{.Calls}}</td><td class="has-text-right">{{usd .USD}}</td></tr>{{else}}<tr><td colspan="3"
class="muted">No model calls.</td></tr>{{end}}
</tbody></table></div>
<h2 class="title is-5">By day, caller, stage and provider</h2>
<div class="table-container"><table class="table is-fullwidth is-hoverable"><thead><tr><th>Day</th><th>Caller</th><th>Stage</th><th>Provider</th><th>Calls</th><th>Spend</th></tr></thead><tbody>
{{range .Rows}}<tr><td>{{day .Day}}</td><td>{{.Caller}}</td><td>{{.Stage}}</td><td>{{.Provider}}</td><td class="has-text-right">{{.Calls}}</td><td
class="has-text-right">{{usd .CostUsd}}</td></tr>{{end}}
</tbody></table></div>
{{end}}{{template "foot"}}{{end}}

{{define "playground"}}{{template "head" .}}{{with .Body}}
<p class="muted">Run a draft rule alone on a PR you can read. It runs every lane step a review would (scoping, the agent, the verifier) and posts nothing on the PR. Start ` +
	`from an existing rule on its page, or paste a rule file.</p>
{{if .Error}}<article class="message is-warning"><p class="message-body">{{.Error}}</p></article>{{end}}
<form method="post" action="/ui/playground"><input type="hidden" name="t" value="{{.Token}}">
<div class="field"><label class="label">PR <input class="input" name="pr" value="{{.PR}}" placeholder="org/repo#123" size="40" required></label></div>
<div class="field"><label class="label" for="rule">Rule file (YAML)</label><div class="control"><textarea class="textarea code" id="rule" name="rule" rows="24"
required>{{.YAML}}</textarea></div></div>
<div class="field"><div class="control"><button class="button is-primary" type="submit">Run</button></div></div></form>
<h2 class="title is-5">Your recent runs</h2>
<div class="table-container"><table class="table is-fullwidth is-hoverable"><thead><tr><th>Run</th><th>Rule</th><th>PR</th><th>Status</th><th>Cost</th><th>When</th></tr></thead><tbody>
{{range .Runs}}<tr><td><a href="/ui/playground/{{.ID}}">{{.ID}}</a></td><td><code>{{.RuleID}}</code></td><td>{{.Repo}}#{{.PrNumber}}</td><td>{{.Status}}</td>` +
	`<td class="has-text-right">{{usd .CostUsd}}</td><td>{{when .CreatedAt}}</td></tr>
{{else}}<tr><td colspan="6" class="muted">No runs yet.</td></tr>{{end}}
</tbody></table></div>
{{end}}{{template "foot"}}{{end}}

{{define "playgroundrun"}}{{template "head" .}}{{with .Body}}
<p>Rule <code>{{.Run.RuleID}}</code> on <a href="{{.PR}}">{{.Run.Repo}}#{{.Run.PrNumber}}</a> · {{.Run.Status}} · {{usd .Run.CostUsd}} · {{when .Run.CreatedAt}}</p>
{{if eq .Run.Status "queued"}}<article class="message is-warning"><p class="message-body">Running. <a href="/ui/playground/{{.Run.ID}}">Reload</a> to see the result.</p></article>{{end}}
{{if .Run.Reason}}<article class="message is-warning"><p class="message-body">{{.Run.Reason}}</p></article>{{end}}
{{with .Result}}{{if not .Ran}}<article class="message is-warning"><p class="message-body">{{.Note}}</p></article>{{else}}
<h2 class="title is-5">Findings ({{$.Body.Kept}} would post)</h2>
<div class="table-container"><table class="table is-fullwidth is-hoverable"><thead><tr><th>Where</th><th>Kept</th><th>Explanation</th></tr></thead><tbody>
{{range .Findings}}<tr><td><code>{{.Path}}:{{.Start}}{{if ne .Start .End}}-{{.End}}{{end}}</code></td><td>{{if .Kept}}yes{{else}}no ({{.Reason}}){{end}}</td><td>{{.Message}}</td></tr>
{{else}}<tr><td colspan="3" class="muted">The rule found nothing on this PR.</td></tr>{{end}}
</tbody></table></div>{{end}}{{end}}
<p><a href="/ui/playground?from={{.Run.ID}}">Edit and run again</a></p>
{{if eq .Run.Status "done"}}<form method="post" action="/ui/actions"><input type="hidden" name="action" value="propose_rule">` +
	`<input type="hidden" name="target" value="{{.Run.ID}}"><input type="hidden" name="t" value="{{.SaveToken}}">
<div class="field is-grouped is-align-items-flex-end"><div class="control is-expanded"><label class="label">Why (optional) <input class="input" name="reason" maxlength="1000"></label></div> ` +
	`<div class="control"><button class="button is-primary" type="submit">Save as a draft config PR</button></div></div>
<p class="help muted">A new rule becomes a candidate; a change to an existing rule needs its owners.</p></form>{{end}}
<h2 class="title is-5">Rule</h2><pre>{{.Run.RuleYaml}}</pre>
{{end}}{{template "foot"}}{{end}}

{{define "notfound"}}{{template "head" .}}<p>There is nothing here, or it is in a repo you cannot read.</p>{{template "foot"}}{{end}}
{{define "error"}}{{template "head" .}}<p>The page could not be loaded. Try again in a minute.</p>{{template "foot"}}{{end}}
`))
