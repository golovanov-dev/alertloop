package api

// Server-rendered HTML for the Community events UI. Styling is inline so the
// page is fully self-contained with no external asset dependencies.

const baseCSS = `
:root { color-scheme: light dark; }
* { box-sizing: border-box; }
body { font-family: system-ui, -apple-system, Segoe UI, Roboto, sans-serif;
  margin: 0; background: #0f1115; color: #e6e6e6; }
a { color: #6ea8fe; text-decoration: none; }
a:hover { text-decoration: underline; }
header { padding: 16px 24px; border-bottom: 1px solid #242832; display: flex;
  align-items: baseline; gap: 16px; }
header h1 { font-size: 18px; margin: 0; }
header .muted { color: #8b93a1; font-size: 13px; }
main { padding: 24px; max-width: 1200px; margin: 0 auto; }
table { width: 100%; border-collapse: collapse; font-size: 14px; }
th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid #242832;
  vertical-align: top; }
th { color: #8b93a1; font-weight: 600; font-size: 12px; text-transform: uppercase;
  letter-spacing: .04em; }
tr:hover td { background: #161a22; }
code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
.badge { display: inline-block; padding: 2px 8px; border-radius: 999px;
  font-size: 12px; font-weight: 600; }
.sev-info { background: #1f2937; color: #93c5fd; }
.sev-success { background: #14311f; color: #86efac; }
.sev-warning { background: #3a2f14; color: #fcd34d; }
.sev-error { background: #3a1b1b; color: #fca5a5; }
.sev-critical { background: #4c1d1d; color: #fecaca; }
.st-new { background: #1f2937; color: #cbd5e1; }
.st-acknowledged { background: #1e3a34; color: #6ee7b7; }
.st-resolved { background: #14311f; color: #86efac; }
.st-muted { background: #2a2a2a; color: #9ca3af; }
.st-escalated { background: #4c1d1d; color: #fecaca; }
.dl-pending { color: #cbd5e1; } .dl-sending { color: #93c5fd; }
.dl-sent { color: #86efac; } .dl-failed { color: #fcd34d; }
.dl-dead_letter { color: #fca5a5; }
.filters { margin-bottom: 16px; display: flex; gap: 8px; flex-wrap: wrap; }
.filters a { padding: 4px 10px; border: 1px solid #242832; border-radius: 6px;
  font-size: 13px; }
.pager { margin-top: 16px; }
.empty { color: #8b93a1; padding: 32px; text-align: center; }
dl { display: grid; grid-template-columns: 160px 1fr; gap: 4px 16px; font-size: 14px; }
dt { color: #8b93a1; } dd { margin: 0; }
pre { background: #161a22; padding: 12px; border-radius: 8px; overflow-x: auto;
  font-size: 12px; }
`

const eventsHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>AlertLoop — Events</title><style>` + baseCSS + `</style></head>
<body>
<header>
  <h1>AlertLoop</h1>
  <span class="muted">Events</span>
  <span class="muted" style="margin-left:auto"><a href="/swagger">API docs</a></span>
</header>
<main>
  <div class="filters">
    <a href="/events?token={{.Token}}">All</a>
    <a href="/events?token={{.Token}}&type=incident">Incidents</a>
    <a href="/events?token={{.Token}}&type=business_event">Business</a>
    <a href="/events?token={{.Token}}&type=audit">Audit</a>
    <a href="/events?token={{.Token}}&state=new">New</a>
    <a href="/events?token={{.Token}}&state=escalated">Escalated</a>
    <a href="/deliveries?token={{.Token}}&state=dead_letter">⚠ Dead-letter</a>
  </div>
  {{if .Events}}
  <table>
    <thead><tr>
      <th>Time</th><th>Type</th><th>Severity</th><th>State</th>
      <th>Source</th><th>Message</th><th>ID</th>
    </tr></thead>
    <tbody>
    {{range .Events}}
      <tr>
        <td><code>{{.CreatedAt.Format "2006-01-02 15:04:05"}}</code></td>
        <td>{{.Type}}</td>
        <td><span class="badge sev-{{.Severity}}">{{.Severity}}</span></td>
        <td><span class="badge st-{{.State}}">{{.State}}</span></td>
        <td>{{.Source}}</td>
        <td>{{.Message}}</td>
        <td><a href="/events/{{.ID}}?token={{$.Token}}"><code>{{shortID .ID}}</code></a></td>
      </tr>
    {{end}}
    </tbody>
  </table>
  {{if .NextCursor}}
  <div class="pager">
    <a href="/events?token={{.Token}}&cursor={{.NextCursor}}">Next page →</a>
  </div>
  {{end}}
  {{else}}
  <div class="empty">No events yet.</div>
  {{end}}
</main>
</body></html>`

const deliveriesHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>AlertLoop — Deliveries</title><style>` + baseCSS + `</style></head>
<body>
<header>
  <h1>AlertLoop</h1>
  <span class="muted">Delivery attempts</span>
  <span class="muted" style="margin-left:auto"><a href="/events?token={{.Token}}">← Events</a></span>
</header>
<main>
  <div class="filters">
    <a href="/deliveries?token={{.Token}}">All</a>
    <a href="/deliveries?token={{.Token}}&state=dead_letter">⚠ Dead-letter</a>
    <a href="/deliveries?token={{.Token}}&state=failed">Failed</a>
    <a href="/deliveries?token={{.Token}}&state=pending">Pending</a>
    <a href="/deliveries?token={{.Token}}&state=sent">Sent</a>
  </div>
  {{if .Deliveries}}
  <table>
    <thead><tr>
      <th>Time</th><th>Channel</th><th>Name</th><th>State</th><th>Attempts</th>
      <th>Last error</th><th>Event</th>
    </tr></thead>
    <tbody>
    {{range .Deliveries}}
      <tr>
        <td><code>{{.CreatedAt.Format "2006-01-02 15:04:05"}}</code></td>
        <td>{{.Channel}}</td>
        <td><code>{{.ChannelName}}</code></td>
        <td class="dl-{{.State}}">{{.State}}</td>
        <td>{{.Attempts}} / {{.MaxAttempts}}</td>
        <td>{{if .LastError}}<code>{{.LastError}}</code>{{else}}—{{end}}</td>
        <td><a href="/events/{{.EventID}}?token={{$.Token}}"><code>{{shortID .EventID}}</code></a></td>
      </tr>
    {{end}}
    </tbody>
  </table>
  {{if .NextCursor}}
  <div class="pager">
    <a href="/deliveries?token={{.Token}}{{if .State}}&state={{.State}}{{end}}&cursor={{.NextCursor}}">Next page →</a>
  </div>
  {{end}}
  {{else}}
  <div class="empty">No delivery attempts{{if .State}} in state "{{.State}}"{{end}}.</div>
  {{end}}
  <p class="muted" style="margin-top:24px">Replay a dead-lettered attempt via the API:
    <code>POST /v1/delivery-attempts/{id}/replay</code></p>
</main>
</body></html>`

const eventDetailHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>AlertLoop — Event {{shortID .Event.ID}}</title><style>` + baseCSS + `</style></head>
<body>
<header>
  <h1>AlertLoop</h1>
  <span class="muted"><a href="/events?token={{.Token}}">← Events</a></span>
</header>
<main>
  <h2>{{.Event.Message}}</h2>
  <dl>
    <dt>ID</dt><dd><code>{{.Event.ID}}</code></dd>
    <dt>Type</dt><dd>{{.Event.Type}}</dd>
    <dt>Severity</dt><dd><span class="badge sev-{{.Event.Severity}}">{{.Event.Severity}}</span></dd>
    <dt>State</dt><dd><span class="badge st-{{.Event.State}}">{{.Event.State}}</span></dd>
    <dt>Source</dt><dd>{{.Event.Source}}</dd>
    {{if .Event.Category}}<dt>Category</dt><dd>{{.Event.Category}}</dd>{{end}}
    {{if .Event.EntityType}}<dt>Entity</dt><dd>{{.Event.EntityType}} / {{.Event.EntityID}}</dd>{{end}}
    {{if .Event.TraceID}}<dt>Trace</dt><dd><code>{{.Event.TraceID}}</code></dd>{{end}}
    {{if .Event.DedupeKey}}<dt>Dedupe key</dt><dd><code>{{.Event.DedupeKey}}</code></dd>{{end}}
    <dt>Created</dt><dd><code>{{.Event.CreatedAt.Format "2006-01-02 15:04:05 MST"}}</code></dd>
    <dt>Updated</dt><dd><code>{{.Event.UpdatedAt.Format "2006-01-02 15:04:05 MST"}}</code></dd>
  </dl>

  <h3>Payload</h3>
  <pre>{{printf "%s" .Event.Payload}}</pre>

  <h3>Delivery attempts</h3>
  {{if .Deliveries}}
  <table>
    <thead><tr>
      <th>Channel</th><th>Name</th><th>State</th><th>Attempts</th><th>Next retry</th>
      <th>Last error</th><th>Updated</th>
    </tr></thead>
    <tbody>
    {{range .Deliveries}}
      <tr>
        <td>{{.Channel}}</td>
        <td><code>{{.ChannelName}}</code></td>
        <td class="dl-{{.State}}">{{.State}}</td>
        <td>{{.Attempts}} / {{.MaxAttempts}}</td>
        <td>{{if .NextRetryAt}}<code>{{.NextRetryAt.Format "15:04:05"}}</code>{{else}}—{{end}}</td>
        <td>{{if .LastError}}<code>{{.LastError}}</code>{{else}}—{{end}}</td>
        <td><code>{{.UpdatedAt.Format "15:04:05"}}</code></td>
      </tr>
    {{end}}
    </tbody>
  </table>
  {{else}}
  <div class="empty">No delivery attempts.</div>
  {{end}}
</main>
</body></html>`
