// Package greetings persists the set of Nostr pubkeys the daemon has already
// greeted so a given agent receives the welcome DM only once.
package greetings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xdamman/agentdesk/internal/config"
)

type Store struct {
	Greeted map[string]time.Time `json:"greeted"`
}

var mu sync.Mutex

func path() (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "greetings.json"), nil
}

func load() (*Store, string, error) {
	p, err := path()
	if err != nil {
		return nil, "", err
	}
	s := &Store{Greeted: map[string]time.Time{}}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return s, p, nil
		}
		return nil, p, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, p, err
	}
	if s.Greeted == nil {
		s.Greeted = map[string]time.Time{}
	}
	return s, p, nil
}

// Has returns true if the pubkey has been greeted before.
func Has(pub string) bool {
	mu.Lock()
	defer mu.Unlock()
	s, _, err := load()
	if err != nil {
		return false
	}
	_, ok := s.Greeted[pub]
	return ok
}

// Mark records that the pubkey has been greeted. Idempotent.
func Mark(pub string) error {
	mu.Lock()
	defer mu.Unlock()
	s, p, err := load()
	if err != nil {
		return err
	}
	if _, ok := s.Greeted[pub]; ok {
		return nil
	}
	s.Greeted[pub] = time.Now().UTC()
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
	return os.WriteFile(p, data, 0o600)
}
