package nostrd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
)

// DefaultRelays mirrors the nostr-cli defaults. Override via
// agentdesk daemon --nostr-relay.
var DefaultRelays = []string{
	"wss://relay.damus.io",
	"wss://nos.lol",
	"wss://relay.primal.net",
	"wss://relay.nostr.band",
	"wss://nostr.mom",
	"wss://relay.mostr.pub",
	"wss://purplepag.es",
}

const (
	connectTimeout = 5 * time.Second
	profileTimeout = 5 * time.Second
	publishTimeout = 3 * time.Second
)

// CardRequest is the NIP-04 payload an agent sends to request a card. Agents
// can submit either JSON or a bare plaintext command (e.g. "request-card").
type CardRequest struct {
	Action    string `json:"action"`              // "request-card" | "get-card"
	Allowance int64  `json:"allowance,omitempty"` // EUR, in cents
	Interval  string `json:"interval,omitempty"`  // daily|weekly|monthly
}

// Handler is invoked for every incoming NIP-04 DM addressed to the daemon. It
// returns the plaintext reply to send back (empty string suppresses the
// reply). The handler receives the sender's resolved profile name when
// available.
type Handler func(ctx context.Context, senderPub, senderName string, req CardRequest) string

// Listener is the long-running Nostr subscriber.
type Listener struct {
	Identity *Identity
	Relays   []string
	Handler  Handler
	Logf     func(format string, a ...any)
}

func (l *Listener) logf(format string, a ...any) {
	if l.Logf != nil {
		l.Logf(format, a...)
		return
	}
	fmt.Printf(format+"\n", a...)
}

// Run blocks until ctx is cancelled.
func (l *Listener) Run(ctx context.Context) error {
	if len(l.Relays) == 0 {
		l.Relays = DefaultRelays
	}
	since := nostr.Now()
	filter := nostr.Filter{
		Kinds: []int{nostr.KindEncryptedDirectMessage}, // 4
		Tags:  nostr.TagMap{"p": []string{l.Identity.PubHex}},
		Since: &since,
	}

	var (
		seenMu sync.Mutex
		seen   = make(map[string]bool)
	)

	var wg sync.WaitGroup
	for _, url := range l.Relays {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			l.relayLoop(ctx, url, filter, &seenMu, seen)
		}(url)
	}

	l.logf("nostr: listening on %d relays for NIP-04 DMs to %s", len(l.Relays), l.Identity.Npub)
	<-ctx.Done()
	wg.Wait()
	return nil
}

func (l *Listener) relayLoop(ctx context.Context, url string, filter nostr.Filter, seenMu *sync.Mutex, seen map[string]bool) {
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
		relay, err := nostr.RelayConnect(connectCtx, url)
		cancel()
		if err != nil {
			l.logf("nostr: %s connect failed: %v (retry in 10s)", url, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
				continue
			}
		}
		l.logf("nostr: connected to %s", url)
		sub, err := relay.Subscribe(ctx, nostr.Filters{filter})
		if err != nil {
			l.logf("nostr: %s subscribe failed: %v", url, err)
			_ = relay.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		l.consume(ctx, relay, sub, url, seenMu, seen)
		sub.Unsub()
		_ = relay.Close()

		if ctx.Err() != nil {
			return
		}
		l.logf("nostr: %s disconnected, reconnecting in 3s", url)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (l *Listener) consume(ctx context.Context, relay *nostr.Relay, sub *nostr.Subscription, url string, seenMu *sync.Mutex, seen map[string]bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events:
			if !ok {
				return
			}
			if ev == nil {
				continue
			}
			seenMu.Lock()
			if seen[ev.ID] {
				seenMu.Unlock()
				continue
			}
			seen[ev.ID] = true
			seenMu.Unlock()
			l.handle(ctx, relay, ev)
		}
	}
}

func (l *Listener) handle(ctx context.Context, relay *nostr.Relay, ev *nostr.Event) {
	plaintext, err := decryptDM(l.Identity.SecretHex, ev)
	if err != nil {
		l.logf("nostr: decrypt from %s failed: %v", ev.PubKey, err)
		return
	}
	plaintext = strings.TrimSpace(plaintext)

	var req CardRequest
	if jErr := json.Unmarshal([]byte(plaintext), &req); jErr != nil || req.Action == "" {
		req = CardRequest{Action: plaintext}
	}

	name := fetchProfileName(ctx, l.Relays, ev.PubKey)
	l.logf("nostr: DM from %s (%s): action=%q", name, shortPub(ev.PubKey), req.Action)

	reply := l.Handler(ctx, ev.PubKey, name, req)
	if reply == "" {
		return
	}
	if err := l.sendReply(ctx, ev.PubKey, reply); err != nil {
		l.logf("nostr: reply to %s failed: %v", shortPub(ev.PubKey), err)
		return
	}
	l.logf("nostr: replied (%d bytes)", len(reply))
}

func (l *Listener) sendReply(ctx context.Context, toPub string, body string) error {
	ss, err := nip04.ComputeSharedSecret(toPub, l.Identity.SecretHex)
	if err != nil {
		return err
	}
	cipher, err := nip04.Encrypt(body, ss)
	if err != nil {
		return err
	}
	ev := nostr.Event{
		PubKey:    l.Identity.PubHex,
		CreatedAt: nostr.Now(),
		Kind:      nostr.KindEncryptedDirectMessage,
		Tags:      nostr.Tags{{"p", toPub}},
		Content:   cipher,
	}
	if err := ev.Sign(l.Identity.SecretHex); err != nil {
		return err
	}
	return publishToAny(ctx, l.Relays, ev)
}

func decryptDM(mySk string, ev *nostr.Event) (string, error) {
	ss, err := nip04.ComputeSharedSecret(ev.PubKey, mySk)
	if err != nil {
		return "", err
	}
	return nip04.Decrypt(ev.Content, ss)
}

// publishToAny publishes the event concurrently to relays and returns nil if
// at least one relay accepted it.
func publishToAny(ctx context.Context, relays []string, ev nostr.Event) error {
	var (
		okCount int
		mu      sync.Mutex
		wg      sync.WaitGroup
		lastErr error
	)
	for _, url := range relays {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
			defer cancel()
			relay, err := nostr.RelayConnect(pubCtx, url)
			if err != nil {
				mu.Lock()
				lastErr = err
				mu.Unlock()
				return
			}
			defer relay.Close()
			if err := relay.Publish(pubCtx, ev); err != nil {
				mu.Lock()
				lastErr = err
				mu.Unlock()
				return
			}
			mu.Lock()
			okCount++
			mu.Unlock()
		}(url)
	}
	wg.Wait()
	if okCount == 0 {
		if lastErr != nil {
			return lastErr
		}
		return errors.New("no relays accepted")
	}
	return nil
}

// fetchProfileName issues a one-shot kind=0 fetch across the relays and
// returns the first "name" / "display_name" it finds.
func fetchProfileName(ctx context.Context, relays []string, pub string) string {
	fctx, cancel := context.WithTimeout(ctx, profileTimeout)
	defer cancel()
	limit := 1
	filter := nostr.Filter{Kinds: []int{0}, Authors: []string{pub}, Limit: limit}

	type res struct {
		name string
	}
	out := make(chan res, len(relays))
	var wg sync.WaitGroup
	for _, url := range relays {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			relay, err := nostr.RelayConnect(fctx, url)
			if err != nil {
				return
			}
			defer relay.Close()
			sub, err := relay.Subscribe(fctx, nostr.Filters{filter})
			if err != nil {
				return
			}
			defer sub.Unsub()
			select {
			case <-fctx.Done():
				return
			case ev, ok := <-sub.Events:
				if !ok || ev == nil {
					return
				}
				var meta struct {
					Name        string `json:"name"`
					DisplayName string `json:"display_name"`
				}
				if err := json.Unmarshal([]byte(ev.Content), &meta); err != nil {
					return
				}
				name := meta.Name
				if name == "" {
					name = meta.DisplayName
				}
				if name != "" {
					select {
					case out <- res{name: name}:
					default:
					}
				}
			}
		}(url)
	}
	go func() { wg.Wait(); close(out) }()

	if r, ok := <-out; ok && r.name != "" {
		return r.name
	}
	return "npub-" + shortPub(pub)
}

func shortPub(p string) string {
	if len(p) >= 8 {
		return p[:8]
	}
	return p
}
