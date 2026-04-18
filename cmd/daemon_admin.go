package cmd

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
	"github.com/nbd-wtf/go-nostr/nip19"

	"github.com/xdamman/agentdesk/internal/config"
	"github.com/xdamman/agentdesk/internal/dmlog"
	"github.com/xdamman/agentdesk/internal/nostrd"
	"github.com/xdamman/agentdesk/internal/store"
	"github.com/xdamman/agentdesk/internal/stripeapi"
)

// Admin profile cache — we resolve the admin's kind-0 profile once and reuse
// the result on the homepage (a fresh relay fetch per page load would be
// slow). AdminProfileRefresh can be invoked after setup changes.
var (
	adminProfileMu   sync.Mutex
	adminProfileName string
	adminProfilePub  string
)

// AdminProfile returns the cached display name for the admin's pubkey, or
// empty if no name has been resolved yet. The display name typically comes
// from a kind-0 profile event on the relays.
func AdminProfile() string {
	adminProfileMu.Lock()
	defer adminProfileMu.Unlock()
	return adminProfileName
}

// RefreshAdminProfile fetches the admin's Nostr kind-0 profile across the
// configured relays in the background. Safe to call repeatedly; the result is
// cached until the admin pubkey changes.
func RefreshAdminProfile(pub string) {
	if pub == "" {
		adminProfileMu.Lock()
		adminProfileName, adminProfilePub = "", ""
		adminProfileMu.Unlock()
		return
	}
	adminProfileMu.Lock()
	if adminProfilePub == pub && adminProfileName != "" {
		adminProfileMu.Unlock()
		return
	}
	adminProfilePub = pub
	adminProfileMu.Unlock()
	go func() {
		relays := daemonNostrRelays
		if len(relays) == 0 {
			relays = nostrd.DefaultRelays
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		name := nostrd.LookupProfileName(ctx, relays, pub)
		adminProfileMu.Lock()
		if adminProfilePub == pub {
			adminProfileName = name
		}
		adminProfileMu.Unlock()
	}()
}

// dispatchNostrDM is the top-level Listener handler. It routes messages from
// the admin (approve/decline replies) separately from card requests from
// agents.
func dispatchNostrDM(ctx context.Context, senderPub, senderName string, req nostrd.CardRequest) string {
	cfg, _ := config.LoadOrEmpty()
	if cfg.AdminNostrPubkey != "" && strings.EqualFold(senderPub, cfg.AdminNostrPubkey) {
		return handleAdminReply(req.Action)
	}
	return nostrHandler(ctx, senderPub, senderName, req)
}

// handleAdminReply parses the admin's free-form DM and, if it looks like an
// approve/decline, pops the oldest pending ask and applies the action. The
// returned string is DM'd back to the admin as a plaintext ack.
func handleAdminReply(text string) string {
	text = strings.TrimSpace(text)
	explicitID := extractAuthID(text)
	intent := parseIntent(text)
	if intent == "" {
		return "Reply `approve` or `decline` (also: yes/no/ok/👍/👎). Append an `iauth_…` id to target a specific request."
	}

	var ask *pendingAdminAsk
	if explicitID != "" {
		ask = popAskByID(explicitID)
		if ask == nil {
			ask = &pendingAdminAsk{AuthID: explicitID}
		}
	} else {
		ask = popOldestAsk()
		if ask == nil {
			return "No pending requests to act on."
		}
	}

	if intent == "approve" {
		if err := doApprove(ask.AuthID); err != nil {
			return summariseForAdmin(ask, "approve failed: "+err.Error())
		}
		return summariseForAdmin(ask, "approved")
	}
	// decline
	if err := doDecline(ask.AuthID); err != nil {
		return summariseForAdmin(ask, "decline failed: "+err.Error())
	}
	return summariseForAdmin(ask, "declined")
}

// parseIntent inspects text and returns "approve", "decline", or "".
func parseIntent(text string) string {
	l := strings.ToLower(strings.TrimSpace(text))
	// Strip leading "approve iauth_xxx" suffix noise by looking at the first
	// whitespace-separated token too.
	first := strings.Fields(l)
	head := ""
	if len(first) > 0 {
		head = first[0]
	}

	approve := map[string]bool{
		"approve": true, "approved": true,
		"yes": true, "y": true,
		"ok": true, "okay": true, "k": true,
		"accept": true, "accepted": true,
		"👍": true, "✅": true, "✓": true, "👌": true,
	}
	decline := map[string]bool{
		"decline": true, "declined": true,
		"no": true, "n": true,
		"reject": true, "rejected": true, "deny": true, "denied": true,
		"👎": true, "❌": true, "✗": true, "🚫": true,
	}

	if approve[l] || approve[head] {
		return "approve"
	}
	if decline[l] || decline[head] {
		return "decline"
	}
	return ""
}

func extractAuthID(text string) string {
	for _, tok := range strings.Fields(text) {
		tok = strings.TrimRight(tok, ".,;:!")
		if strings.HasPrefix(tok, "iauth_") && len(tok) > 8 {
			return tok
		}
	}
	return ""
}

// --- pending admin asks (FIFO) ---

const pendingAskMaxAge = 5 * time.Minute

func rememberAdminAsk(ask pendingAdminAsk) {
	pendingAdminAsksMu.Lock()
	defer pendingAdminAsksMu.Unlock()
	// GC old entries so a forgotten pile-up doesn't live forever.
	cutoff := time.Now().Add(-pendingAskMaxAge)
	kept := pendingAdminAsks[:0]
	for _, a := range pendingAdminAsks {
		if a.CreatedAt.After(cutoff) {
			kept = append(kept, a)
		}
	}
	pendingAdminAsks = append(kept, ask)
}

func popOldestAsk() *pendingAdminAsk {
	pendingAdminAsksMu.Lock()
	defer pendingAdminAsksMu.Unlock()
	if len(pendingAdminAsks) == 0 {
		return nil
	}
	ask := pendingAdminAsks[0]
	pendingAdminAsks = pendingAdminAsks[1:]
	return &ask
}

func popAskByID(id string) *pendingAdminAsk {
	pendingAdminAsksMu.Lock()
	defer pendingAdminAsksMu.Unlock()
	for i, a := range pendingAdminAsks {
		if a.AuthID == id {
			pendingAdminAsks = append(pendingAdminAsks[:i], pendingAdminAsks[i+1:]...)
			return &a
		}
	}
	return nil
}

func summariseForAdmin(ask *pendingAdminAsk, verb string) string {
	if ask.Merchant == "" && ask.Amount == 0 {
		return fmt.Sprintf("%s: %s", verb, ask.AuthID)
	}
	return fmt.Sprintf("%s: %s — €%.2f @ %s (%s)",
		verb, ask.Agent, float64(ask.Amount)/100, ask.Merchant, ask.AuthID)
}

// askAdminAboutAuth DMs the admin about a pending authorization and remembers
// the ask so the admin's reply can be matched. Called from the webhook
// handler. It runs inline (within the webhook's 2s budget where possible); if
// the reply arrives in time doApprove will succeed, else our existing expired-
// approve flow records a rule.
func askAdminAboutAuth(cfg *config.Config, authID, agentName, merchant string, amount int64) {
	if cfg.AdminNostrPubkey == "" || daemonIdentity == nil {
		return
	}
	relays := daemonNostrRelays
	if len(relays) == 0 {
		relays = nostrd.DefaultRelays
	}

	ask := pendingAdminAsk{
		AuthID:    authID,
		Agent:     agentName,
		Merchant:  merchant,
		Amount:    amount,
		CreatedAt: time.Now(),
	}
	rememberAdminAsk(ask)

	body := fmt.Sprintf(
		"🔔 %s wants to charge €%.2f @ %s\n"+
			"Reply \"approve\" to allow, \"decline\" to block.\n"+
			"auth: %s",
		agentName, float64(amount)/100, merchant, authID,
	)

	go func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sendAdminDM(sendCtx, cfg.AdminNostrPubkey, body, relays); err != nil {
			logf("admin: DM to %s failed: %v", adminDisplay(cfg), err)
			dmlog.Record(dmlog.Entry{
				Dir: dmlog.DirOut, Peer: cfg.AdminNostrPubkey,
				PeerName: adminDisplayShort(cfg),
				Note:     "admin ask (send failed): " + err.Error(),
				Preview:  body,
			})
			return
		}
		logf("admin: DM'd %s about %s", adminDisplay(cfg), authID)
		dmlog.Record(dmlog.Entry{
			Dir: dmlog.DirOut, Peer: cfg.AdminNostrPubkey,
			PeerName: adminDisplayShort(cfg),
			Note:     "admin ask for " + authID,
			Preview:  body,
		})
	}()
}

// adminDisplayShort returns a short label for the admin suitable for the DM
// log column (NIP-05 if set, otherwise first 8 chars of npub).
func adminDisplayShort(c *config.Config) string {
	if c.AdminNIP05 != "" {
		return c.AdminNIP05
	}
	if np, err := nip19.EncodePublicKey(c.AdminNostrPubkey); err == nil && len(np) > 12 {
		return np[:12] + "…"
	}
	return ""
}

func sendAdminDM(ctx context.Context, toPub, text string, relays []string) error {
	ss, err := nip04.ComputeSharedSecret(toPub, daemonIdentity.SecretHex)
	if err != nil {
		return err
	}
	cipher, err := nip04.Encrypt(text, ss)
	if err != nil {
		return err
	}
	ev := nostr.Event{
		PubKey:    daemonIdentity.PubHex,
		CreatedAt: nostr.Now(),
		Kind:      nostr.KindEncryptedDirectMessage,
		Tags:      nostr.Tags{{"p", toPub}},
		Content:   cipher,
	}
	if err := ev.Sign(daemonIdentity.SecretHex); err != nil {
		return err
	}
	// publish across relays; any one accepting is enough
	var last error
	ok := false
	for _, url := range relays {
		relay, rErr := nostr.RelayConnect(ctx, url)
		if rErr != nil {
			last = rErr
			continue
		}
		if pErr := relay.Publish(ctx, ev); pErr != nil {
			last = pErr
			relay.Close()
			continue
		}
		relay.Close()
		ok = true
	}
	if !ok {
		if last != nil {
			return last
		}
		return fmt.Errorf("no relays accepted")
	}
	return nil
}

// adminDisplay returns a short string for logging/UI identifying the admin.
func adminDisplay(c *config.Config) string {
	if c.AdminNIP05 != "" {
		return c.AdminNIP05
	}
	if c.AdminNostrPubkey == "" {
		return ""
	}
	if np, err := nip19.EncodePublicKey(c.AdminNostrPubkey); err == nil {
		return np
	}
	return c.AdminNostrPubkey
}

// agentNameForAuthEvent is shared by webhook + admin flows.
func agentNameForAuthEvent(s *store.Store, cardID, cardholderName string) string {
	if cardID != "" {
		if a := s.FindByCard(cardID); a != nil {
			return a.Name
		}
	}
	if cardholderName != "" {
		return cardholderName
	}
	return "—"
}

// Bind import at compile-time.
var _ = stripeapi.FormatAuthorizationStatus
