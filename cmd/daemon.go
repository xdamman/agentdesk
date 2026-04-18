package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	stripe "github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"

	"github.com/xdamman/agentdesk/internal/config"
	"github.com/xdamman/agentdesk/internal/greetings"
	"github.com/xdamman/agentdesk/internal/nostrd"
	"github.com/xdamman/agentdesk/internal/rules"
	"github.com/xdamman/agentdesk/internal/store"
	"github.com/xdamman/agentdesk/internal/stripeapi"
)

var (
	daemonPort       int
	daemonSecret     string
	daemonPath       string
	daemonSkipVerify bool

	daemonNostrDisable bool
	daemonNostrRelays  []string

	daemonRegisterURL  string
	daemonRegisterJSON bool

	// creationMu serialises cardholder+card creation per Nostr sender so a
	// retrying agent can't race and get two cards.
	creationMu sync.Mutex

	// daemonIdentity is the running daemon's Nostr identity; set at startup.
	// Used by the homepage handler and the admin DM dispatcher.
	daemonIdentity *nostrd.Identity

	// pendingAdminAsks is a FIFO of authorization IDs we've DM'd the admin
	// about and are awaiting a reply on. When the admin replies "approve" (or
	// "yes"/"ok"/"👍"), the oldest ask is popped and approved.
	pendingAdminAsks    []pendingAdminAsk
	pendingAdminAsksMu  sync.Mutex
)

type pendingAdminAsk struct {
	AuthID    string
	Agent     string
	Merchant  string
	Amount    int64
	CreatedAt time.Time
}

func resolvedPort(cfg *config.Config) int {
	if daemonPort != 0 {
		return daemonPort
	}
	if cfg != nil && cfg.DaemonPort != 0 {
		return cfg.DaemonPort
	}
	return 4242
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonRegisterCmd)

	daemonCmd.Flags().IntVar(&daemonPort, "port", 0, "HTTP port to listen on (default: config.daemon_port or 4242)")
	daemonCmd.Flags().StringVar(&daemonSecret, "secret", "", "Webhook signing secret (default: config.webhook_secret)")
	daemonCmd.Flags().StringVar(&daemonPath, "path", "/webhook", "HTTP path to serve the webhook on")
	daemonCmd.Flags().BoolVar(&daemonSkipVerify, "insecure-skip-verify", false, "Skip webhook signature verification (dev only)")
	daemonCmd.Flags().BoolVar(&daemonNostrDisable, "no-nostr", false, "Disable the Nostr NIP-04 listener")
	daemonCmd.Flags().StringSliceVar(&daemonNostrRelays, "nostr-relay", nil, "Nostr relay URL (repeatable; default: public relays)")

	daemonRegisterCmd.Flags().StringVar(&daemonRegisterURL, "url", "", "Publicly reachable webhook URL (required)")
	daemonRegisterCmd.Flags().BoolVar(&daemonRegisterJSON, "json", false, "Print registration result as JSON")
}

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the webhook daemon that auto-approves matching requests",
	Long: `Run the webhook daemon that listens for Stripe Issuing authorization
webhooks and auto-approves requests that match a saved rule within the
Stripe-enforced 2-second window.

Two ways to point Stripe at this daemon:

1. Local development (recommended): use the Stripe CLI's listen command,
   which streams webhook events to a local port without a public URL:

     stripe listen --forward-to localhost:4242/webhook
     agentdesk daemon --port 4242 --secret $(stripe listen --print-secret)

2. Production: expose the daemon on a public URL (e.g. via ngrok or a real
   deployment) and register it with Stripe using:

     agentdesk daemon register --url https://example.com/webhook

   Then run: agentdesk daemon`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// The daemon starts even without a Stripe key so the user can finish
		// onboarding via the web /setup page. Stripe-dependent features stay
		// inert until the config is written.
		cfg, _ := config.LoadOrEmpty()
		if cfg.StripeAPIKey != "" {
			stripeapi.Init(cfg.StripeAPIKey)
		} else {
			logf("agentdesk is not configured — visit http://localhost:%d/setup to finish onboarding", resolvedPort(cfg))
		}

		port := resolvedPort(cfg)

		secret := daemonSecret
		if secret == "" {
			secret = cfg.WebhookSecret
		}
		// Only enforce the secret once Stripe is configured; during first-run
		// web onboarding the /webhook endpoint is effectively dormant.
		if cfg.StripeAPIKey != "" && secret == "" && !daemonSkipVerify {
			return fmt.Errorf("webhook secret not set — pass --secret or run `agentdesk daemon register` first, or use --insecure-skip-verify for dev")
		}

		h := &webhookHandler{secret: secret, skipVerify: daemonSkipVerify}
		mux := http.NewServeMux()
		mux.HandleFunc(daemonPath, h.serve)
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "ok")
		})
		mux.HandleFunc("/setup", setupHandler)
		mux.HandleFunc("/", homeHandler)

		addr := fmt.Sprintf(":%d", port)
		srv := &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      5 * time.Second,
		}

		logf("agentdesk daemon listening on %s%s", addr, daemonPath)
		if daemonSkipVerify {
			logf("warning: running with --insecure-skip-verify — signatures NOT checked")
		}
		rs, _ := rules.Load()
		logf("loaded %d auto-approve rule(s)", len(rs.Rules))

		rootCtx, cancelRoot := context.WithCancel(context.Background())
		defer cancelRoot()

		// Optional Nostr listener runs in parallel with the webhook server.
		if !daemonNostrDisable {
			id, err := nostrd.LoadOrGenerate()
			if err != nil {
				return fmt.Errorf("load nostr key: %w", err)
			}
			daemonIdentity = id
			logf("nostr identity: %s", id.Npub)
			logf("  secret key stored at ~/.agentdesk/nsec (0600)")
			if cfg.AdminNostrPubkey != "" {
				logf("admin approver: %s", adminDisplay(cfg))
				RefreshAdminProfile(cfg.AdminNostrPubkey)
			} else {
				logf("admin approver: not set — run `agentdesk setup --admin <npub|nip05>` to enable approvals")
			}
			relays := daemonNostrRelays
			if len(relays) == 0 {
				relays = nostrd.DefaultRelays
			}
			listener := &nostrd.Listener{
				Identity: id,
				Relays:   relays,
				Handler:  dispatchNostrDM,
				Logf:     logf,
			}
			go func() {
				if err := listener.Run(rootCtx); err != nil {
					logf("nostr listener exited: %v", err)
				}
			}()
		}

		// Graceful shutdown on SIGINT/SIGTERM.
		errCh := make(chan error, 1)
		go func() { errCh <- srv.ListenAndServe() }()

		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		case <-sigs:
			logf("shutting down...")
			cancelRoot()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
		}
		return nil
	},
}

// welcomeMessage is the daemon's very first reply to any npub it doesn't
// recognise. It points the agent at the SKILL doc; the agent should then
// retry its request.
const welcomeMessage = `👋 Welcome to agentdesk. I issue Stripe virtual cards to AI agents over Nostr DMs.

Before sending requests, please read the skill so you know how to talk to me, store the card, and retry on failure:

https://github.com/xdamman/agentdesk/blob/main/SKILL.md

When you're ready, send me:

    request-card

…or the JSON form described in the skill. I'll reply with the card details.`

// nostrHandler handles a NIP-04 card request from an agent. It looks up any
// existing agent tied to the sender's pubkey (idempotent) or creates a new
// one using the sender's Nostr profile name and the default billing identity.
// The reply is plain text (see SKILL.md for the shape).
func nostrHandler(ctx context.Context, senderPub, senderName string, req nostrd.CardRequest) string {
	agents, err := store.Load()
	if err != nil {
		return "⚠ Error: failed to load agent store. Please try again."
	}

	// First contact: welcome the agent and point them at the skill. Only
	// skip this for senders that already have a card — we don't re-greet
	// established agents, even if we have no local greeting record yet.
	if agents.FindByNostrPubkey(senderPub) == nil && !greetings.Has(senderPub) {
		if err := greetings.Mark(senderPub); err != nil {
			logf("nostr: mark greeting for %s failed: %v", senderPub[:8], err)
		}
		logf("nostr: welcomed new pubkey %s", senderPub[:8])
		return welcomeMessage
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action != "request-card" && action != "get-card" {
		return fmt.Sprintf("⚠ Unknown action %q. Send `request-card` to issue a card. See https://github.com/xdamman/agentdesk/blob/main/SKILL.md", req.Action)
	}

	creationMu.Lock()
	defer creationMu.Unlock()

	if existing := agents.FindByNostrPubkey(senderPub); existing != nil {
		card, err := stripeapi.RevealCard(existing.CardID)
		if err != nil {
			logf("nostr: reveal existing card for %s failed: %v", existing.Name, err)
			return "⚠ Error: failed to reveal your existing card. Please try again."
		}
		return formatCardMessage(existing, card, "Card on file")
	}

	if action == "get-card" {
		return "⚠ No card on file for this npub yet. Send `request-card` first."
	}

	cents, interval := resolveAllowance(req)
	name := agentNameFromNostr(agents, senderName, senderPub)

	cfg, _ := config.LoadOrEmpty()
	if !cfg.Billing.Complete() {
		logf("nostr: billing not configured — using fallback identity")
	}

	ch, err := stripeapi.CreateCardholder(name, cfg.Billing)
	if err != nil {
		logf("nostr: create cardholder for %s failed: %v", name, err)
		return "⚠ Error: cardholder creation failed. Please try again."
	}
	card, err := stripeapi.CreateVirtualCard(ch.ID, name, cents, interval, nil)
	if err != nil {
		logf("nostr: create card for %s failed: %v", name, err)
		return "⚠ Error: card creation failed. Please try again."
	}
	revealed, err := stripeapi.RevealCard(card.ID)
	if err != nil {
		logf("nostr: reveal card for %s failed: %v", name, err)
		return "⚠ Error: card reveal failed. Please try again."
	}

	agent := store.Agent{
		Name:         name,
		CardholderID: ch.ID,
		CardID:       card.ID,
		Last4:        card.Last4,
		Brand:        string(card.Brand),
		Allowance:    store.Allowance{Amount: cents, Interval: interval},
		NostrPubkey:  senderPub,
		CreatedAt:    time.Now().UTC(),
	}
	agents.Upsert(agent)
	if err := agents.Save(); err != nil {
		logf("nostr: save agent store failed: %v", err)
		return "⚠ Error: card created but local store failed. Please try again."
	}
	logf("nostr: created card for %s (card=%s, npub=%s)", name, card.ID, senderPub[:8])
	return formatCardMessage(&agent, revealed, "Card created")
}

func resolveAllowance(req nostrd.CardRequest) (int64, string) {
	cents := req.Allowance
	if cents <= 0 {
		cents = 10000 // €100 default
	}
	interval := strings.ToLower(strings.TrimSpace(req.Interval))
	if !validDaemonIntervals[interval] {
		interval = "monthly"
	}
	return cents, interval
}

var validDaemonIntervals = map[string]bool{"daily": true, "weekly": true, "monthly": true}

// agentNameFromNostr derives a unique, Stripe-friendly agent name from the
// sender's Nostr profile name, falling back to the npub prefix.
func agentNameFromNostr(s *store.Store, profileName, pub string) string {
	base := sanitizeAgentName(profileName)
	if base == "" {
		base = "npub-" + pub[:8]
	}
	candidate := base
	for i := 2; s.Find(candidate) != nil; i++ {
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
	return candidate
}

var nonNameRune = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func sanitizeAgentName(name string) string {
	name = strings.TrimSpace(name)
	name = nonNameRune.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if len(name) > 32 {
		name = name[:32]
	}
	if len(name) < 2 {
		return ""
	}
	return name
}

// formatCardMessage renders a plaintext reply carrying the card details.
// `headline` is e.g. "Card created" or "Card on file".
func formatCardMessage(a *store.Agent, card *stripe.IssuingCard, headline string) string {
	number := card.Number
	if number == "" {
		number = "(retrieve via CLI)"
	} else {
		number = spaceEvery4(number)
	}
	currency := strings.ToUpper(string(card.Currency))
	if currency == "" {
		currency = "EUR"
	}
	return fmt.Sprintf(`✓ %s for %s.

Brand:    %s
Number:   %s
CVC:      %s
Expires:  %02d/%d
Last 4:   %s
Currency: %s
Policy:   €%.2f / %s

Card ID: %s

Save these details locally (0600) and never re-emit them. If a purchase pends approval, retry per the skill: https://github.com/xdamman/agentdesk/blob/main/SKILL.md`,
		headline, a.Name,
		card.Brand, number, card.CVC,
		card.ExpMonth, card.ExpYear,
		card.Last4, currency,
		float64(a.Allowance.Amount)/100, a.Allowance.Interval,
		card.ID,
	)
}

var daemonRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Register a webhook endpoint with Stripe and save the signing secret",
	Long: `Register a publicly reachable webhook endpoint with Stripe (via the
/v1/webhook_endpoints API) and save the returned signing secret to
~/.agentdesk/config.json so that 'agentdesk daemon' can verify events.

You must supply a public HTTPS URL (e.g. from ngrok, cloudflared, or your
production host). For local development without a public URL, skip this
command and use 'stripe listen --forward-to localhost:4242/webhook' instead.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireConfig(); err != nil {
			return err
		}
		if daemonRegisterURL == "" {
			return fmt.Errorf("--url is required")
		}
		if !strings.HasPrefix(daemonRegisterURL, "https://") {
			return fmt.Errorf("--url must be https://")
		}
		ep, err := stripeapi.CreateWebhookEndpoint(daemonRegisterURL)
		if err != nil {
			return err
		}
		cfg, _ := config.LoadOrEmpty()
		cfg.WebhookSecret = ep.Secret
		cfg.WebhookURL = ep.URL
		if err := config.Save(cfg); err != nil {
			return err
		}
		if daemonRegisterJSON {
			return printJSON(map[string]any{
				"id":             ep.ID,
				"url":            ep.URL,
				"secret":         ep.Secret,
				"enabled_events": ep.EnabledEvents,
			})
		}
		fmt.Printf("✓ Registered %s\n  id:     %s\n  secret: %s (saved to config)\n",
			ep.URL, ep.ID, ep.Secret)
		fmt.Println("  You can now run `agentdesk daemon`.")
		return nil
	},
}

// ---- webhook handler ----

type webhookHandler struct {
	secret     string
	skipVerify bool
}

func (h *webhookHandler) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var event stripe.Event
	if h.skipVerify {
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
	} else {
		ev, err := webhook.ConstructEvent(body, r.Header.Get("Stripe-Signature"), h.secret)
		if err != nil {
			logf("signature verification failed: %v", err)
			http.Error(w, "signature verification failed", http.StatusUnauthorized)
			return
		}
		event = ev
	}

	switch event.Type {
	case "issuing_authorization.request":
		h.handleRequest(w, event)
	case "issuing_authorization.created", "issuing_authorization.updated":
		h.logAuth(event)
		w.WriteHeader(http.StatusOK)
	default:
		// Ignore other events silently.
		w.WriteHeader(http.StatusOK)
	}
}

func (h *webhookHandler) handleRequest(w http.ResponseWriter, event stripe.Event) {
	var auth stripe.IssuingAuthorization
	if err := json.Unmarshal(event.Data.Raw, &auth); err != nil {
		logf("cannot decode authorization: %v", err)
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}

	cardID := ""
	if auth.Card != nil {
		cardID = auth.Card.ID
	}
	merchant := merchantName(&auth)
	date := rules.DateFromUnix(auth.Created)
	amount := auth.Amount

	rs, err := rules.Load()
	if err != nil {
		logf("load rules: %v", err)
		http.Error(w, "rules load failed", http.StatusInternalServerError)
		return
	}

	agents, _ := store.Load()
	agentName := "—"
	if a := agents.FindByCard(cardID); a != nil {
		agentName = a.Name
	}

	logf("request: agent=%s merchant=%q amount=€%.2f card=%s auth=%s",
		agentName, merchant, float64(amount)/100, cardID, auth.ID)

	rule := rs.Match(cardID, merchant, amount, date)
	if rule == nil {
		logf("  → no matching rule; letting Stripe spending_controls decide")
		cfg, _ := config.LoadOrEmpty()
		switch {
		case cfg.AdminNostrPubkey == "":
			logf("  → admin not configured, skipping DM (run `agentdesk setup --admin <npub|nip05>`)")
		case daemonIdentity == nil:
			logf("  → nostr listener disabled (--no-nostr), cannot DM admin")
		default:
			askAdminAboutAuth(cfg, auth.ID, agentName, merchant, amount)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	logf("  → matched rule %s", rule.ID)

	approved, err := stripeapi.ApproveAuthorization(auth.ID)
	if err != nil {
		logf("  → rule %s matched but approve failed: %v", rule.ID, err)
		http.Error(w, "approve failed", http.StatusInternalServerError)
		return
	}
	_ = rs.RecordMatch(rule.ID)
	logf("  ✓ auto-approved by rule %s (status=%s)", rule.ID, stripeapi.FormatAuthorizationStatus(approved))
	w.WriteHeader(http.StatusOK)
}

func (h *webhookHandler) logAuth(event stripe.Event) {
	var auth stripe.IssuingAuthorization
	if err := json.Unmarshal(event.Data.Raw, &auth); err != nil {
		return
	}
	logf("%s: %s merchant=%q amount=€%.2f status=%s",
		event.Type, auth.ID, merchantName(&auth), float64(auth.Amount)/100,
		stripeapi.FormatAuthorizationStatus(&auth))
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[%s] %s\n",
		time.Now().Format("15:04:05"),
		fmt.Sprintf(format, a...))
}
