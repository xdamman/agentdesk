// Package dmlog is a small in-memory ring buffer of recent Nostr DM events,
// surfaced on the daemon's homepage for debugging. Nothing here is persisted
// to disk — the buffer resets on process restart.
package dmlog

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// Direction of the DM relative to the daemon.
const (
	DirIn  = "in"
	DirOut = "out"
)

// Entry is one recorded DM, trimmed to a first-line preview. We never store
// the full plaintext because outgoing card replies contain PAN/CVC.
type Entry struct {
	Time     time.Time
	Dir      string // "in" | "out"
	Peer     string // hex pubkey
	PeerName string // display name resolved from kind 0, may be ""
	Note     string // "webhook: sent ask", "card created", etc. (optional context)
	Preview  string // first line, sensitive fields redacted
}

var (
	mu   sync.Mutex
	ring []Entry
	max  = 100
)

// Record appends an entry to the buffer, trimming the oldest when the ring
// hits capacity.
func Record(e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Preview = preview(e.Preview)
	mu.Lock()
	ring = append(ring, e)
	if len(ring) > max {
		ring = ring[len(ring)-max:]
	}
	mu.Unlock()
}

// Recent returns up to n most-recent entries, newest first.
func Recent(n int) []Entry {
	mu.Lock()
	defer mu.Unlock()
	if n <= 0 || n > len(ring) {
		n = len(ring)
	}
	out := make([]Entry, n)
	for i := 0; i < n; i++ {
		out[i] = ring[len(ring)-1-i]
	}
	return out
}

// preview returns the first line of body, truncated, with card-number-looking
// and CVC-looking fragments masked. Defensive — replies should already be
// free of secrets by the time we log them, but we redact to be safe.
func preview(body string) string {
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[:i]
	}
	body = panRE.ReplaceAllString(body, "•••• •••• •••• ••••")
	body = cvcRE.ReplaceAllString(body, "CVC: •••")
	if len([]rune(body)) > 140 {
		r := []rune(body)
		body = string(r[:140]) + "…"
	}
	return strings.TrimSpace(body)
}

var (
	panRE = regexp.MustCompile(`\b\d{4}[ -]?\d{4}[ -]?\d{4}[ -]?\d{4}\b`)
	cvcRE = regexp.MustCompile(`(?i)cvc\s*[:=]\s*\d{3,4}`)
)
