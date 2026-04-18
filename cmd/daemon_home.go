package cmd

import (
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v82"

	"github.com/xdamman/agentdesk/internal/config"
	"github.com/xdamman/agentdesk/internal/store"
	"github.com/xdamman/agentdesk/internal/stripeapi"

	qrcode "github.com/skip2/go-qrcode"
)

// homeHandler renders the daemon's status page: npub + QR code, agents, and
// recent authorization requests. When agentdesk has not been configured yet,
// it redirects to /setup so the user can onboard via the browser.
func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if cfg, _ := config.LoadOrEmpty(); cfg == nil || cfg.StripeAPIKey == "" {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	data := homeData{Now: time.Now().UTC().Format("2006-01-02 15:04:05 UTC")}
	if r.URL.Query().Get("saved") == "1" {
		data.Notice = "Settings saved."
	}
	if daemonIdentity != nil {
		data.Npub = daemonIdentity.Npub
		if png, err := qrcode.Encode(daemonIdentity.Npub, qrcode.Medium, 280); err == nil {
			// template.URL marks this as a trusted URL so html/template doesn't
			// rewrite the data: scheme to #ZgotmplZ.
			data.QRDataURL = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
		}
	}

	agents, _ := store.Load()
	for _, a := range agents.Agents {
		spent, _ := stripeapi.SpentThisPeriod(a.CardID, a.Allowance.Interval)
		data.Agents = append(data.Agents, agentRow{
			Name:      a.Name,
			Last4:     a.Last4,
			Brand:     a.Brand,
			Allowance: fmt.Sprintf("€%.2f / %s", float64(a.Allowance.Amount)/100, a.Allowance.Interval),
			Spent:     fmt.Sprintf("€%.2f", float64(spent)/100),
			Remaining: fmt.Sprintf("€%.2f", float64(a.Allowance.Amount-spent)/100),
			NostrPub:  shortenPub(a.NostrPubkey),
		})
	}

	if auths, err := stripeapi.ListAuthorizations(25); err == nil {
		for _, a := range auths {
			data.Requests = append(data.Requests, requestRow{
				ID:       a.ID,
				When:     time.Unix(a.Created, 0).Format("Jan 02 15:04"),
				Agent:    agentForAuthHTML(agents, a),
				Merchant: merchantName(a),
				Amount:   fmt.Sprintf("€%.2f", float64(a.Amount)/100),
				Status:   stripeapi.FormatAuthorizationStatus(a),
			})
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = homeTemplate.Execute(w, data)
}

func agentForAuthHTML(s *store.Store, a *stripe.IssuingAuthorization) string {
	if a.Card != nil {
		if found := s.FindByCard(a.Card.ID); found != nil {
			return found.Name
		}
		if a.Card.Metadata != nil {
			if v, ok := a.Card.Metadata["agentdesk_agent"]; ok && v != "" {
				return v
			}
		}
	}
	if a.Cardholder != nil && a.Cardholder.Name != "" {
		return a.Cardholder.Name
	}
	return "—"
}

func shortenPub(p string) string {
	if p == "" {
		return ""
	}
	if len(p) <= 12 {
		return p
	}
	return p[:8] + "…"
}

// --- template ---

type homeData struct {
	Npub      string
	QRDataURL template.URL
	Agents    []agentRow
	Requests  []requestRow
	Now       string
	Notice    string
}

type agentRow struct {
	Name      string
	Last4     string
	Brand     string
	Allowance string
	Spent     string
	Remaining string
	NostrPub  string
}

type requestRow struct {
	ID       string
	When     string
	Agent    string
	Merchant string
	Amount   string
	Status   string
}

var homeTemplate = template.Must(template.New("home").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>agentdesk</title>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    :root {
      --bg: #0f0f12;
      --fg: #eaeaea;
      --muted: #8a8a90;
      --accent: #d28bff;
      --card: #18181d;
      --border: #2a2a30;
    }
    * { box-sizing: border-box; }
    html, body { margin: 0; padding: 0; background: var(--bg); color: var(--fg); font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
    .wrap { max-width: 980px; margin: 0 auto; padding: 32px 20px 80px; }
    header { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 24px; }
    header h1 { margin: 0; font-size: 20px; letter-spacing: -0.01em; }
    header .now { color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
    section { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 20px 24px; margin-bottom: 20px; }
    section h2 { margin: 0 0 14px; font-size: 15px; letter-spacing: 0.02em; text-transform: uppercase; color: var(--muted); font-weight: 600; }
    .npub-row { display: flex; gap: 24px; align-items: center; }
    .npub-row .qr { width: 140px; height: 140px; background: #fff; padding: 6px; border-radius: 8px; flex: 0 0 auto; }
    .npub-row .qr img { width: 100%; height: 100%; display: block; }
    .npub-row code { display: block; word-break: break-all; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; padding: 10px 12px; font: 12px/1.4 ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--accent); }
    .npub-row p { margin: 0 0 10px; color: var(--muted); }
    table { width: 100%; border-collapse: collapse; }
    th, td { text-align: left; padding: 10px 12px; border-bottom: 1px solid var(--border); font-variant-numeric: tabular-nums; }
    th { color: var(--muted); font-weight: 600; font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; }
    tr:last-child td { border-bottom: none; }
    td.mono, .mono { font: 12px ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--muted); }
    .badge { display: inline-block; padding: 2px 8px; border-radius: 999px; font-size: 11px; background: #2a2a30; }
    .badge.approved { background: #17432c; color: #7ee0a8; }
    .badge.declined { background: #452020; color: #ff8585; }
    .badge.pending  { background: #3f3620; color: #ffd785; }
    .empty { color: var(--muted); font-style: italic; padding: 20px 0; }
  </style>
</head>
<body>
  <div class="wrap">
    <header>
      <h1>agentdesk</h1>
      <span class="now"><a href="/setup" style="color: var(--muted); text-decoration: none; margin-right: 14px;">settings</a>{{.Now}}</span>
    </header>

    {{if .Notice}}
    <section style="background: #17432c; color: #7ee0a8; border-color: #224e38;">{{.Notice}}</section>
    {{end}}

    <section>
      <h2>Daemon identity</h2>
      {{if .Npub}}
      <div class="npub-row">
        {{if .QRDataURL}}<div class="qr"><img src="{{.QRDataURL}}" alt="QR code for npub"></div>{{end}}
        <div>
          <p>Agents message this daemon on Nostr (NIP-04 DM) to request a virtual card:</p>
          <code>{{.Npub}}</code>
        </div>
      </div>
      {{else}}
        <p class="empty">Nostr listener is disabled.</p>
      {{end}}
    </section>

    <section>
      <h2>Agents</h2>
      {{if .Agents}}
      <table>
        <thead><tr><th>Name</th><th>Card</th><th>Policy</th><th>Spent</th><th>Remaining</th><th>Nostr</th></tr></thead>
        <tbody>
        {{range .Agents}}
          <tr>
            <td>{{.Name}}</td>
            <td class="mono">{{.Brand}} ••{{.Last4}}</td>
            <td>{{.Allowance}}</td>
            <td>{{.Spent}}</td>
            <td>{{.Remaining}}</td>
            <td class="mono">{{if .NostrPub}}{{.NostrPub}}{{else}}—{{end}}</td>
          </tr>
        {{end}}
        </tbody>
      </table>
      {{else}}
        <p class="empty">No agents yet — create one with <span class="mono">agentdesk agents add</span> or via a Nostr DM.</p>
      {{end}}
    </section>

    <section>
      <h2>Recent requests</h2>
      {{if .Requests}}
      <table>
        <thead><tr><th>When</th><th>Agent</th><th>Merchant</th><th>Amount</th><th>Status</th><th>ID</th></tr></thead>
        <tbody>
        {{range .Requests}}
          <tr>
            <td>{{.When}}</td>
            <td>{{.Agent}}</td>
            <td>{{.Merchant}}</td>
            <td>{{.Amount}}</td>
            <td><span class="badge {{.Status}}">{{.Status}}</span></td>
            <td class="mono">{{.ID}}</td>
          </tr>
        {{end}}
        </tbody>
      </table>
      {{else}}
        <p class="empty">No authorization requests yet.</p>
      {{end}}
    </section>
  </div>
</body>
</html>
`))

// Silence unused import checks if platform trims them later.
var _ = strings.TrimSpace
