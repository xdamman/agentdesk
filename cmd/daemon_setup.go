package cmd

import (
	"fmt"
	"html/template"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/nbd-wtf/go-nostr/nip19"

	"github.com/xdamman/agentdesk/internal/config"
	"github.com/xdamman/agentdesk/internal/stripeapi"
)

// setupHandler exposes the daemon's web setup page. GET renders a form with
// the current values prefilled; POST validates the submitted form, saves the
// new config, re-initialises the Stripe client, and redirects back to /. POST
// is restricted to loopback callers so that exposing the daemon publicly (via
// ngrok / --register-url) doesn't let anyone rewrite the Stripe key.
func setupHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		renderSetupForm(w, nil, nil, "")
	case http.MethodPost:
		if !isLoopbackRequest(r) {
			http.Error(w, "web setup is only accessible from the loopback interface", http.StatusForbidden)
			return
		}
		handleSetupPost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type setupForm struct {
	APIKey     string
	AdminID    string
	AdminHex   string
	FirstName  string
	LastName   string
	DOB        string
	Email      string
	Phone      string
	Line1      string
	Line2      string
	City       string
	State      string
	PostalCode string
	Country    string
}

func formFromConfig(c *config.Config) setupForm {
	f := setupForm{Country: "FR"}
	if c == nil {
		return f
	}
	f.APIKey = c.StripeAPIKey
	if c.AdminNIP05 != "" {
		f.AdminID = c.AdminNIP05
	} else if c.AdminNostrPubkey != "" {
		if np, err := nip19.EncodePublicKey(c.AdminNostrPubkey); err == nil {
			f.AdminID = np
		} else {
			f.AdminID = c.AdminNostrPubkey
		}
	}
	f.AdminHex = c.AdminNostrPubkey
	if c.Billing != nil {
		b := c.Billing
		f.FirstName = b.FirstName
		f.LastName = b.LastName
		f.DOB = b.DOB
		f.Email = b.Email
		f.Phone = b.PhoneNumber
		f.Line1 = b.Line1
		f.Line2 = b.Line2
		f.City = b.City
		f.State = b.State
		f.PostalCode = b.PostalCode
		if b.Country != "" {
			f.Country = b.Country
		}
	}
	return f
}

func formFromRequest(r *http.Request) setupForm {
	g := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	return setupForm{
		APIKey:     g("api_key"),
		AdminID:    g("admin"),
		FirstName:  g("first_name"),
		LastName:   g("last_name"),
		DOB:        g("dob"),
		Email:      g("email"),
		Phone:      g("phone"),
		Line1:      g("line1"),
		Line2:      g("line2"),
		City:       g("city"),
		State:      g("state"),
		PostalCode: g("postal_code"),
		Country:    strings.ToUpper(g("country")),
	}
}

// applySetupFormToConfig validates a submitted form and merges it into cfg.
// Returns either a field-keyed map of errors or nil.
func applySetupFormToConfig(f setupForm, cfg *config.Config) map[string]string {
	errs := map[string]string{}

	if f.APIKey == "" {
		errs["api_key"] = "required"
	} else if !strings.HasPrefix(f.APIKey, "sk_") {
		errs["api_key"] = "expected a Stripe secret key starting with sk_"
	}

	// billing fields required together
	if f.FirstName == "" {
		errs["first_name"] = "required"
	}
	if f.LastName == "" {
		errs["last_name"] = "required"
	}
	if f.DOB == "" {
		errs["dob"] = "required"
	} else if _, _, _, ok := config.ParseDOB(f.DOB); !ok {
		errs["dob"] = "expected YYYY-MM-DD"
	}
	if f.Line1 == "" {
		errs["line1"] = "required"
	}
	if f.City == "" {
		errs["city"] = "required"
	}
	if f.PostalCode == "" {
		errs["postal_code"] = "required"
	}
	if f.Country == "" {
		errs["country"] = "required"
	} else if !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(f.Country) {
		errs["country"] = "expected 2-letter ISO code (e.g. FR)"
	}

	// Admin is optional. If provided, must resolve.
	var adminHex, adminDisplayStr string
	if f.AdminID != "" {
		hex, disp, err := resolveNostrIdentity(f.AdminID)
		if err != nil {
			errs["admin"] = err.Error()
		} else {
			adminHex, adminDisplayStr = hex, disp
		}
	}

	if len(errs) > 0 {
		return errs
	}

	cfg.StripeAPIKey = f.APIKey
	cfg.Billing = &config.Billing{
		FirstName:   f.FirstName,
		LastName:    f.LastName,
		DOB:         f.DOB,
		Email:       f.Email,
		PhoneNumber: f.Phone,
		Line1:       f.Line1,
		Line2:       f.Line2,
		City:        f.City,
		State:       f.State,
		PostalCode:  f.PostalCode,
		Country:     f.Country,
	}
	cfg.AdminNostrPubkey = adminHex
	cfg.AdminNIP05 = adminDisplayStr
	return nil
}

func handleSetupPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	cfg, _ := config.LoadOrEmpty()
	form := formFromRequest(r)
	if errs := applySetupFormToConfig(form, cfg); errs != nil {
		renderSetupForm(w, &form, errs, "")
		return
	}
	if err := config.Save(cfg); err != nil {
		renderSetupForm(w, &form, map[string]string{"_": err.Error()}, "")
		return
	}
	// Re-init the Stripe client so subsequent API calls use the new key.
	stripeapi.Init(cfg.StripeAPIKey)
	http.Redirect(w, r, "/?saved=1", http.StatusSeeOther)
}

func renderSetupForm(w http.ResponseWriter, submitted *setupForm, errs map[string]string, notice string) {
	cfg, _ := config.LoadOrEmpty()
	var form setupForm
	if submitted != nil {
		form = *submitted
	} else {
		form = formFromConfig(cfg)
	}
	if errs == nil {
		errs = map[string]string{}
	}

	data := setupPageData{
		Form:        form,
		Errors:      errs,
		Notice:      notice,
		IsFirstRun:  cfg == nil || cfg.StripeAPIKey == "",
		NeedsBanner: cfg == nil || cfg.StripeAPIKey == "",
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := setupTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type setupPageData struct {
	Form        setupForm
	Errors      map[string]string
	Notice      string
	IsFirstRun  bool
	NeedsBanner bool
}

// isLoopbackRequest returns true if the request originated from 127.0.0.0/8
// or ::1, so we refuse to save config via a publicly exposed URL.
func isLoopbackRequest(r *http.Request) bool {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func fieldErr(errs map[string]string, key string) string {
	return errs[key]
}

var setupTemplate = template.Must(template.New("setup").Funcs(template.FuncMap{
	"err": fieldErr,
}).Parse(setupHTML))

// setupHTML — inlined for a single-binary deploy.
const setupHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>agentdesk · setup</title>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    :root {
      --bg: #0f0f12;
      --fg: #eaeaea;
      --muted: #8a8a90;
      --accent: #d28bff;
      --card: #18181d;
      --border: #2a2a30;
      --err: #ff8585;
      --ok: #7ee0a8;
    }
    * { box-sizing: border-box; }
    html, body { margin: 0; padding: 0; background: var(--bg); color: var(--fg); font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
    .wrap { max-width: 680px; margin: 0 auto; padding: 32px 20px 80px; }
    h1 { margin: 0 0 6px; font-size: 22px; letter-spacing: -0.01em; }
    p.lede { color: var(--muted); margin: 0 0 24px; }
    .banner { background: #3f3620; color: #ffd785; border: 1px solid #665325; border-radius: 8px; padding: 12px 14px; margin-bottom: 20px; }
    .banner.ok { background: #17432c; color: var(--ok); border-color: #224e38; }
    fieldset { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 18px 22px 22px; margin: 0 0 20px; }
    legend { padding: 0 6px; color: var(--muted); font-size: 11px; text-transform: uppercase; letter-spacing: 0.08em; font-weight: 600; }
    .row { display: grid; gap: 10px; margin-bottom: 12px; }
    .row.two { grid-template-columns: 1fr 1fr; }
    label { display: block; font-size: 12px; color: var(--muted); margin-bottom: 4px; }
    input[type=text], input[type=password], input[type=email] {
      width: 100%; padding: 9px 11px;
      background: var(--bg); color: var(--fg);
      border: 1px solid var(--border); border-radius: 6px;
      font: inherit;
    }
    input:focus { outline: none; border-color: var(--accent); }
    .fielderr { color: var(--err); font-size: 11px; margin-top: 4px; }
    .actions { display: flex; gap: 10px; justify-content: flex-end; margin-top: 8px; }
    button, a.btn {
      background: var(--accent); color: #1a0a2e; border: 0; border-radius: 6px;
      padding: 9px 18px; font: inherit; font-weight: 600; cursor: pointer; text-decoration: none;
    }
    a.btn.ghost { background: transparent; color: var(--fg); border: 1px solid var(--border); }
    .hint { color: var(--muted); font-size: 11px; margin-top: 4px; }
  </style>
</head>
<body>
  <div class="wrap">
    <h1>agentdesk setup</h1>
    <p class="lede">Configure the Stripe API key, default cardholder identity, and optional Nostr admin approver.</p>

    {{if .NeedsBanner}}
    <div class="banner">agentdesk isn't configured yet — fill the form below to finish onboarding.</div>
    {{end}}
    {{if .Notice}}
    <div class="banner ok">{{.Notice}}</div>
    {{end}}
    {{if err .Errors "_"}}
    <div class="banner" style="color: var(--err);">{{err .Errors "_"}}</div>
    {{end}}

    <form method="POST" action="/setup">
      <fieldset>
        <legend>Stripe</legend>
        <div class="row">
          <label for="api_key">Secret key</label>
          <input type="password" id="api_key" name="api_key" value="{{.Form.APIKey}}" placeholder="sk_test_..." autocomplete="off">
          {{with err .Errors "api_key"}}<div class="fielderr">{{.}}</div>{{end}}
          <div class="hint">Use a restricted key with Issuing read/write.</div>
        </div>
      </fieldset>

      <fieldset>
        <legend>Admin approver (Nostr, optional)</legend>
        <div class="row">
          <label for="admin">npub, hex pubkey, or NIP-05 address</label>
          <input type="text" id="admin" name="admin" value="{{.Form.AdminID}}" placeholder="npub1… or alice@example.com">
          {{with err .Errors "admin"}}<div class="fielderr">{{.}}</div>{{end}}
          <div class="hint">When set, the daemon DMs this identity for every authorization request and accepts "approve"/"decline" replies.</div>
        </div>
      </fieldset>

      <fieldset>
        <legend>Default cardholder identity</legend>
        <div class="row two">
          <div>
            <label for="first_name">First name</label>
            <input type="text" id="first_name" name="first_name" value="{{.Form.FirstName}}">
            {{with err .Errors "first_name"}}<div class="fielderr">{{.}}</div>{{end}}
          </div>
          <div>
            <label for="last_name">Last name</label>
            <input type="text" id="last_name" name="last_name" value="{{.Form.LastName}}">
            {{with err .Errors "last_name"}}<div class="fielderr">{{.}}</div>{{end}}
          </div>
        </div>
        <div class="row two">
          <div>
            <label for="dob">Date of birth (YYYY-MM-DD)</label>
            <input type="text" id="dob" name="dob" value="{{.Form.DOB}}" placeholder="1985-07-24">
            {{with err .Errors "dob"}}<div class="fielderr">{{.}}</div>{{end}}
          </div>
          <div>
            <label for="email">Email (optional)</label>
            <input type="email" id="email" name="email" value="{{.Form.Email}}">
          </div>
        </div>
        <div class="row">
          <label for="phone">Phone (optional, E.164)</label>
          <input type="text" id="phone" name="phone" value="{{.Form.Phone}}" placeholder="+33612345678">
        </div>
      </fieldset>

      <fieldset>
        <legend>Billing address</legend>
        <div class="row">
          <label for="line1">Line 1</label>
          <input type="text" id="line1" name="line1" value="{{.Form.Line1}}">
          {{with err .Errors "line1"}}<div class="fielderr">{{.}}</div>{{end}}
        </div>
        <div class="row">
          <label for="line2">Line 2 (optional)</label>
          <input type="text" id="line2" name="line2" value="{{.Form.Line2}}">
        </div>
        <div class="row two">
          <div>
            <label for="city">City</label>
            <input type="text" id="city" name="city" value="{{.Form.City}}">
            {{with err .Errors "city"}}<div class="fielderr">{{.}}</div>{{end}}
          </div>
          <div>
            <label for="state">State / region (optional)</label>
            <input type="text" id="state" name="state" value="{{.Form.State}}">
          </div>
        </div>
        <div class="row two">
          <div>
            <label for="postal_code">Postal code</label>
            <input type="text" id="postal_code" name="postal_code" value="{{.Form.PostalCode}}">
            {{with err .Errors "postal_code"}}<div class="fielderr">{{.}}</div>{{end}}
          </div>
          <div>
            <label for="country">Country (ISO 3166-1 alpha-2)</label>
            <input type="text" id="country" name="country" value="{{.Form.Country}}" maxlength="2" style="text-transform: uppercase;">
            {{with err .Errors "country"}}<div class="fielderr">{{.}}</div>{{end}}
          </div>
        </div>
      </fieldset>

      <div class="actions">
        {{if not .IsFirstRun}}<a class="btn ghost" href="/">Cancel</a>{{end}}
        <button type="submit">Save</button>
      </div>
    </form>
  </div>
</body>
</html>
`

// Guarantee fmt import is retained even if template printing changes.
var _ = fmt.Sprintf
