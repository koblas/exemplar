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
<title>{{.Title}} · Exemplar</title>
<style>
:root{color-scheme:light dark}
body{font:15px/1.5 system-ui,sans-serif;margin:0;padding:0 1rem 3rem;max-width:72rem;margin-inline:auto}
header{display:flex;flex-wrap:wrap;gap:1rem;align-items:baseline;border-bottom:1px solid #8884;padding:.75rem 0;margin-bottom:1rem}
header strong{margin-right:1rem}header a{text-decoration:none}header a.on{font-weight:600;text-decoration:underline}
header form{margin-left:auto}button{font:inherit}
table{border-collapse:collapse;width:100%;margin:.5rem 0 1.5rem}th,td{text-align:left;padding:.3rem .5rem;border-bottom:1px solid #8883;vertical-align:top}
th{font-weight:600}td.n{text-align:right;font-variant-numeric:tabular-nums}
code,pre{font-family:ui-monospace,monospace;font-size:13px}pre{white-space:pre-wrap;background:#8881;padding:.5rem;border-radius:4px}
.notice{background:#f5c54222;border:1px solid #f5c54288;padding:.5rem;border-radius:4px}
.muted{opacity:.7}form.filters{display:flex;gap:.5rem;flex-wrap:wrap;align-items:center}
</style></head><body>
<header><strong>Exemplar</strong>
<a href="/ui/" {{if eq .Nav "rules"}}class="on"{{end}}>Rules</a>
<a href="/ui/candidates" {{if eq .Nav "candidates"}}class="on"{{end}}>Candidates</a>
<a href="/ui/audit" {{if eq .Nav "audit"}}class="on"{{end}}>Auto-approve audit</a>
<a href="/ui/cost" {{if eq .Nav "cost"}}class="on"{{end}}>Cost</a>
<a href="/ui/playground" {{if eq .Nav "playground"}}class="on"{{end}}>Playground</a>
<a href="/ui/actions" {{if eq .Nav "actions"}}class="on"{{end}}>Your actions</a>
<form method="post" action="/ui/signout"><span class="muted">{{.Login}}</span> <button type="submit">Sign out</button></form>
</header>
{{if .Notice}}<p class="notice">{{.Notice}}</p>{{end}}
<h1>{{.Title}}</h1>
{{end}}
{{define "foot"}}</body></html>{{end}}

{{define "rules"}}{{template "head" .}}{{with .Body}}
<form class="filters" method="get" action="/ui/">
<label>Owner <select name="owner"><option value="">any</option>{{range .Owners}}<option {{if eq . $.Body.Owner}}selected{{end}}>{{.}}</option>{{end}}</select></label>
<label>Status <select name="status"><option value="">any</option>{{range .Statuses}}<option {{if eq . $.Body.Status}}selected{{end}}>{{.}}</option>{{end}}</select></label>
<label>Search <input name="q" value="{{.Search}}"></label> <button type="submit">Filter</button>
</form>
<p class="muted">Outcomes over the last {{.Window}} days. Effective FP is weighted (spec §9.2).</p>
<table><thead><tr><th>Rule</th><th>Status</th><th>Severity</th><th>Category</th><th>Owner</th><th>Posted</th><th>Acceptance</th><th>Effective FP</th><th></th></tr></thead><tbody>
{{range .Rules}}<tr><td><a href="/ui/rules/{{.RuleID}}"><code>{{.RuleID}}</code></a>{{if ne .Scope "global"}} <span class="muted">({{.Scope}})</span>{{end}}{{if .Exported}} ` +
	`<span class="muted">exported</span>{{end}}</td>
<td>{{.Status}}</td><td>{{.Severity}}</td><td>{{.Category}}</td><td>{{.Owner}}</td><td class="n">{{.Posted}}</td><td class="n">{{.Acceptance}}</td><td class="n">{{.EFP}}</td><td>{{.Health}}</td></tr>
{{else}}<tr><td colspan="9" class="muted">No rules match.</td></tr>{{end}}
</tbody></table>
{{end}}{{template "foot"}}{{end}}

{{define "rule"}}{{template "head" .}}{{with .Body}}
<p>{{.Rule.Status}} · {{.Rule.Severity}} · {{.Rule.Category}} · {{.Rule.Kind}} · owner {{.Rule.Owner}}{{if .Rule.Exported}} · exported to coding agents{{end}}</p>
<p>Last 28 days: {{.Rule.Posted}} posted, acceptance {{.Rule.Acceptance}}, effective FP {{.Rule.EFP}}.</p>
{{if eq .Rule.Scope "global"}}<p><a href="/ui/playground?rule={{.Rule.RuleID}}">Try a change to this rule in the playground</a></p>{{end}}
{{if and (eq .Rule.Scope "global") (ne .Rule.Status "killed")}}<details><summary>Owner actions</summary>
<p class="muted">Each opens an exemplar-config PR in your name and merges it where branch protection allows. Only the rule's owners and the platform team can.</p>
{{if eq .Rule.Status "active"}}<form method="post" action="/ui/actions"><input type="hidden" name="action" value="shadow_rule">` +
	`<input type="hidden" name="target" value="{{.Rule.RuleID}}"><input type="hidden" name="t" value="{{.ShadowToken}}">
<label>Why (optional) <input name="reason" maxlength="1000"></label> <button type="submit">Move to shadow</button></form>{{end}}
<form method="post" action="/ui/actions"><input type="hidden" name="action" value="kill_rule"><input type="hidden" name="target" value="{{.Rule.RuleID}}">` +
	`<input type="hidden" name="t" value="{{.KillToken}}">
<label>Why <input name="reason" maxlength="1000" required></label> <button type="submit">Kill</button></form>
</details>{{end}}
<h2>By week</h2>
<table><thead><tr><th>Week</th><th>Posted</th><th>Accepted</th><th>Rejected</th><th>Ignored</th><th>Acceptance</th></tr></thead><tbody>
{{range .Weeks}}<tr><td>{{day .Week}}</td><td class="n">{{.Posted}}</td><td class="n">{{.Accepted}}</td><td class="n">{{.Rejected}}</td><td class="n">{{.Ignored}}</td>` +
	`<td class="n">{{ratio .Acceptance}}</td></tr>
{{else}}<tr><td colspan="6" class="muted">No weeks yet.</td></tr>{{end}}
</tbody></table>
<h2>Recent findings</h2>
<table><thead><tr><th>Finding</th><th>PR</th><th>Where</th><th>State</th><th>Outcome</th><th>Posted</th></tr></thead><tbody>
{{range .Findings}}<tr><td><a href="/ui/findings/{{.ID}}">{{.ID}}</a></td><td><a href="{{.PR}}">{{.Repo}}#{{.PrNumber}}</a></td><td><code>{{.Path}}:{{.StartLine}}</code></td>` +
	`<td>{{.State}}</td><td>{{.Outcome}}</td><td>{{day .CreatedAt}}</td></tr>
{{else}}<tr><td colspan="6" class="muted">None you can see.</td></tr>{{end}}
</tbody></table>
{{if .Hidden}}<p class="muted">{{.Hidden}} more in repos you cannot read.</p>{{end}}
{{end}}{{template "foot"}}{{end}}

{{define "finding"}}{{template "head" .}}{{with .Body}}
<p>Rule <a href="/ui/rules/{{.F.RuleID}}"><code>{{.F.RuleID}}</code></a> ({{.F.Lane}}, {{.F.Severity}}) on <a href="{{.PR}}">{{.F.Repo}}#{{.F.PrNumber}}</a>,
<code>{{.F.Path}}:{{.F.StartLine}}{{if ne .F.StartLine .F.EndLine}}-{{.F.EndLine}}{{end}}</code> ({{.F.Side}}).
{{if .Comment}}<a href="{{.Comment}}">The comment on GitHub</a>.{{end}}</p>
<p>State {{.F.State}}{{if .F.SuppressedReason}} ({{.F.SuppressedReason}}){{end}}{{if .F.VerifyVerdict}} · verifier {{.F.VerifyVerdict}}{{end}} · {{when .F.CreatedAt}}</p>
{{if .Recorded}}<p class="notice">Recorded as a false positive. Rule owners see it in their digest.</p>{{end}}
<h2>Explanation</h2><pre>{{.F.Message}}</pre>
{{if .F.SuggestedFix}}<h2>Suggested fix{{if not .F.FixValid}} <span class="muted">(not validated; not posted)</span>{{end}}</h2><pre>{{.F.SuggestedFix}}</pre>{{end}}
<h2>Feedback</h2>
<table><thead><tr><th>When</th><th>Signal</th><th>Who</th><th>Role</th><th>Weight</th><th>Via</th><th>Said</th></tr></thead><tbody>
{{range .Events}}<tr><td>{{when .ObservedAt}}</td><td>{{.Kind}}</td><td>{{.Actor}}</td><td>{{.Role}}</td><td class="n">{{.Weight}}</td><td>{{.Via}}</td><td>{{.ReasonText}}</td></tr>
{{else}}<tr><td colspan="7" class="muted">No feedback yet.</td></tr>{{end}}
</tbody></table>
<form method="post" action="/ui/findings/{{.F.ID}}/false-positive"><input type="hidden" name="t" value="{{.Token}}">
<label>Why it is wrong (optional) <input name="text" maxlength="1000"></label> <button type="submit">Mark false positive</button></form>
{{end}}{{template "foot"}}{{end}}

{{define "candidates"}}{{template "head" .}}
<p class="muted">Open candidates by score, with the repos you can read among those their source comments came from.</p>
<table><thead><tr><th>Score</th><th>Candidate</th><th>Cluster</th><th>Reviewers</th><th>Owner</th><th>Status</th><th>Repos</th><th></th></tr></thead><tbody>
{{range .Body}}<tr><td class="n">{{printf "%.2f" .Score}}</td><td><strong>{{.Title}}</strong><br><span class="muted">{{.Description}}</span>{{if .ConfigPrUrl}}<br>` +
	`<a href="{{.ConfigPrUrl}}">Config PR</a>{{end}}</td>
<td class="n">{{.ClusterSize}}</td><td class="n">{{.DistinctReviewers}}</td><td>{{.Owner}}</td><td>{{.Status}}</td><td>{{range $i, $r := .Visible}}{{if $i}}, {{end}}{{$r}}{{end}}</td>
<td><form method="post" action="/ui/actions"><input type="hidden" name="action" value="kill_candidate"><input type="hidden" name="target" value="{{.ID}}">` +
	`<input type="hidden" name="t" value="{{.Token}}">
<input name="reason" maxlength="1000" required placeholder="why"> <button type="submit">Kill</button></form></td></tr>
{{else}}<tr><td colspan="8" class="muted">No open candidates you can see.</td></tr>{{end}}
</tbody></table>
{{template "foot"}}{{end}}

{{define "audit"}}{{template "head" .}}{{with .Body}}
<p class="muted">The latest 500 production decisions, in repos you can read. Each links to its PR.</p>
{{if .Repos}}<details><summary>Kill switches</summary>
<p class="muted">Turns auto-approve off for a repo through an exemplar-config PR in your name, merged where branch protection allows. The platform team and the repo's ` +
	`approvable owners can. Turning it back on is a reviewed config PR.</p>
<table><tbody>{{range .Repos}}<tr><td>{{.Repo}}</td><td><form method="post" action="/ui/actions"><input type="hidden" name="action" value="kill_switch">` +
	`<input type="hidden" name="target" value="{{.Repo}}"><input type="hidden" name="t" value="{{.Token}}">
<input name="reason" maxlength="1000" placeholder="why (optional)"> <button type="submit">Turn auto-approve off</button></form></td></tr>{{end}}</tbody></table>
</details>{{end}}
<table><thead><tr><th>When</th><th>PR</th><th>Head</th><th>Decision</th><th>Stage 1 reasons</th><th>Stage 2</th></tr></thead><tbody>
{{range .Decisions}}<tr><td>{{when .At}}</td><td><a href="{{.PR}}">{{.Repo}}#{{.PrNumber}}</a></td><td><code>{{short .HeadSha}}</code></td><td>{{.Decision}}</td>` +
	`<td>{{.Stage1Reasons}}</td><td>{{.Stage2}}</td></tr>
{{else}}<tr><td colspan="6" class="muted">No decisions you can see.</td></tr>{{end}}
</tbody></table>
{{end}}{{template "foot"}}{{end}}

{{define "actions"}}{{template "head" .}}
<p class="muted">Your last 50 owner actions. Each runs in a worker job, which checks you may take it; reload to see the outcome.</p>
<table><thead><tr><th>Requested</th><th>Action</th><th>Target</th><th>Reason</th><th>Status</th><th>Result</th><th>PR</th></tr></thead><tbody>
{{range .Body}}<tr><td>{{when .CreatedAt}}</td><td>{{.Action}}</td><td><code>{{.Target}}</code></td><td>{{.Reason}}</td><td>{{.Status}}</td>` +
	`<td>{{if .Result}}{{.Result}}{{end}}</td><td>{{if .PrUrl}}<a href="{{.PrUrl}}">PR</a>{{end}}</td></tr>
{{else}}<tr><td colspan="7" class="muted">No actions yet.</td></tr>{{end}}
</tbody></table>
{{template "foot"}}{{end}}

{{define "cost"}}{{template "head" .}}{{with .Body}}
<form class="filters" method="get" action="/ui/cost"><label>Days <input name="days" type="number" min="1" max="365" value="{{.Days}}"></label> <button type="submit">Show</button></form>
<p>{{usd .Total}} over the last {{.Days}} days. Cost is informational (PRD §13.2).</p>
<h2>By caller</h2>
<table><thead><tr><th>Caller</th><th>Calls</th><th>Spend</th></tr></thead><tbody>
{{range .ByCaller}}<tr><td>{{.Key}}</td><td class="n">{{.Calls}}</td><td class="n">{{usd .USD}}</td></tr>{{else}}<tr><td colspan="3" class="muted">No model calls.</td></tr>{{end}}
</tbody></table>
<h2>By day, caller, stage and provider</h2>
<table><thead><tr><th>Day</th><th>Caller</th><th>Stage</th><th>Provider</th><th>Calls</th><th>Spend</th></tr></thead><tbody>
{{range .Rows}}<tr><td>{{day .Day}}</td><td>{{.Caller}}</td><td>{{.Stage}}</td><td>{{.Provider}}</td><td class="n">{{.Calls}}</td><td class="n">{{usd .CostUsd}}</td></tr>{{end}}
</tbody></table>
{{end}}{{template "foot"}}{{end}}

{{define "playground"}}{{template "head" .}}{{with .Body}}
<p class="muted">Run a draft rule alone on a PR you can read. It runs every lane step a review would (scoping, the agent, the verifier) and posts nothing on the PR. Start ` +
	`from an existing rule on its page, or paste a rule file.</p>
{{if .Error}}<p class="notice">{{.Error}}</p>{{end}}
<form method="post" action="/ui/playground"><input type="hidden" name="t" value="{{.Token}}">
<p><label>PR <input name="pr" value="{{.PR}}" placeholder="org/repo#123" size="40" required></label></p>
<p><label for="rule">Rule file (YAML)</label><br><textarea id="rule" name="rule" rows="24" style="width:100%;font-family:ui-monospace,monospace" required>{{.YAML}}</textarea></p>
<p><button type="submit">Run</button></p></form>
<h2>Your recent runs</h2>
<table><thead><tr><th>Run</th><th>Rule</th><th>PR</th><th>Status</th><th>Cost</th><th>When</th></tr></thead><tbody>
{{range .Runs}}<tr><td><a href="/ui/playground/{{.ID}}">{{.ID}}</a></td><td><code>{{.RuleID}}</code></td><td>{{.Repo}}#{{.PrNumber}}</td><td>{{.Status}}</td>` +
	`<td class="n">{{usd .CostUsd}}</td><td>{{when .CreatedAt}}</td></tr>
{{else}}<tr><td colspan="6" class="muted">No runs yet.</td></tr>{{end}}
</tbody></table>
{{end}}{{template "foot"}}{{end}}

{{define "playgroundrun"}}{{template "head" .}}{{with .Body}}
<p>Rule <code>{{.Run.RuleID}}</code> on <a href="{{.PR}}">{{.Run.Repo}}#{{.Run.PrNumber}}</a> · {{.Run.Status}} · {{usd .Run.CostUsd}} · {{when .Run.CreatedAt}}</p>
{{if eq .Run.Status "queued"}}<p class="notice">Running. <a href="/ui/playground/{{.Run.ID}}">Reload</a> to see the result.</p>{{end}}
{{if .Run.Reason}}<p class="notice">{{.Run.Reason}}</p>{{end}}
{{with .Result}}{{if not .Ran}}<p class="notice">{{.Note}}</p>{{else}}
<h2>Findings ({{$.Body.Kept}} would post)</h2>
<table><thead><tr><th>Where</th><th>Kept</th><th>Explanation</th></tr></thead><tbody>
{{range .Findings}}<tr><td><code>{{.Path}}:{{.Start}}{{if ne .Start .End}}-{{.End}}{{end}}</code></td><td>{{if .Kept}}yes{{else}}no ({{.Reason}}){{end}}</td><td>{{.Message}}</td></tr>
{{else}}<tr><td colspan="3" class="muted">The rule found nothing on this PR.</td></tr>{{end}}
</tbody></table>{{end}}{{end}}
<p><a href="/ui/playground?from={{.Run.ID}}">Edit and run again</a></p>
{{if eq .Run.Status "done"}}<form method="post" action="/ui/actions"><input type="hidden" name="action" value="propose_rule">` +
	`<input type="hidden" name="target" value="{{.Run.ID}}"><input type="hidden" name="t" value="{{.SaveToken}}">
<label>Why (optional) <input name="reason" maxlength="1000"></label> <button type="submit">Save as a draft config PR</button>
<span class="muted">A new rule becomes a candidate; a change to an existing rule needs its owners.</span></form>{{end}}
<h2>Rule</h2><pre>{{.Run.RuleYaml}}</pre>
{{end}}{{template "foot"}}{{end}}

{{define "notfound"}}{{template "head" .}}<p>There is nothing here, or it is in a repo you cannot read.</p>{{template "foot"}}{{end}}
{{define "error"}}{{template "head" .}}<p>The page could not be loaded. Try again in a minute.</p>{{template "foot"}}{{end}}
`))
