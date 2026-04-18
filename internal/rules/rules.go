// Package rules stores and matches auto-approval rules.
//
// A rule is created when the user runs `agentdesk requests approve <id>` but
// the authorization is already closed (the 2-second window has expired). The
// daemon then uses these rules to auto-approve future matching
// issuing_authorization.request webhooks within the window.
//
// A request matches a rule when card_id, merchant name, amount (cents), and
// calendar date (UTC) are all equal.
package rules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xdamman/agentdesk/internal/config"
)

type Rule struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent,omitempty"`
	CardID    string    `json:"card_id"`
	Merchant  string    `json:"merchant"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency,omitempty"`
	Date      string    `json:"date"` // YYYY-MM-DD in UTC
	CreatedAt time.Time `json:"created_at"`
	// LastMatched is set to the last time this rule auto-approved a request.
	LastMatched *time.Time `json:"last_matched,omitempty"`
}

type Store struct {
	Rules []Rule `json:"rules"`
}

func path() (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "rules.json"), nil
}

func Load() (*Store, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &Store{}, nil
		}
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Store) Save() error {
	d, err := config.Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(d, "rules.json")
	return os.WriteFile(p, data, 0o600)
}

// Match returns the first rule matching the given fields, or nil.
func (s *Store) Match(cardID, merchant string, amount int64, date string) *Rule {
	for i := range s.Rules {
		r := &s.Rules[i]
		if r.CardID != cardID {
			continue
		}
		if !strings.EqualFold(r.Merchant, merchant) {
			continue
		}
		if r.Amount != amount {
			continue
		}
		if r.Date != date {
			continue
		}
		return r
	}
	return nil
}

// Add appends a rule if no exact duplicate exists; returns the rule that is
// now in the store (new or pre-existing) and whether it was newly added.
func (s *Store) Add(r Rule) (*Rule, bool) {
	if existing := s.Match(r.CardID, r.Merchant, r.Amount, r.Date); existing != nil {
		return existing, false
	}
	if r.ID == "" {
		r.ID = fmt.Sprintf("rule_%d", time.Now().UnixNano())
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	s.Rules = append(s.Rules, r)
	return &s.Rules[len(s.Rules)-1], true
}

// RemoveByID removes a rule by id; returns true if it existed.
func (s *Store) RemoveByID(id string) bool {
	for i, r := range s.Rules {
		if r.ID == id {
			s.Rules = append(s.Rules[:i], s.Rules[i+1:]...)
			return true
		}
	}
	return false
}

// RecordMatch updates the rule's LastMatched timestamp in-place and persists.
func (s *Store) RecordMatch(id string) error {
	for i := range s.Rules {
		if s.Rules[i].ID == id {
			now := time.Now().UTC()
			s.Rules[i].LastMatched = &now
			return s.Save()
		}
	}
	return nil
}

// TodayUTC returns the current date in YYYY-MM-DD (UTC).
func TodayUTC() string {
	return time.Now().UTC().Format("2006-01-02")
}

// DateFromUnix returns the YYYY-MM-DD (UTC) for a unix timestamp.
func DateFromUnix(ts int64) string {
	return time.Unix(ts, 0).UTC().Format("2006-01-02")
}
