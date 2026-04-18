package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xdamman/agentdesk/internal/config"
)

type Allowance struct {
	Amount   int64  `json:"amount"`   // minor units (cents)
	Interval string `json:"interval"` // daily | weekly | monthly
}

type Agent struct {
	Name             string    `json:"name"`
	CardholderID     string    `json:"cardholder_id"`
	CardID           string    `json:"card_id"`
	Last4            string    `json:"last4"`
	Brand            string    `json:"brand"`
	Allowance        Allowance `json:"allowance"`
	AllowedMerchants []string  `json:"allowed_merchants,omitempty"`
	NostrPubkey      string    `json:"nostr_pubkey,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type Store struct {
	Agents []Agent `json:"agents"`
}

func path() (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "agents.json"), nil
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
	p := filepath.Join(d, "agents.json")
	return os.WriteFile(p, data, 0o600)
}

func (s *Store) Find(name string) *Agent {
	for i := range s.Agents {
		if s.Agents[i].Name == name {
			return &s.Agents[i]
		}
	}
	return nil
}

func (s *Store) FindByCard(cardID string) *Agent {
	for i := range s.Agents {
		if s.Agents[i].CardID == cardID {
			return &s.Agents[i]
		}
	}
	return nil
}

func (s *Store) FindByNostrPubkey(pub string) *Agent {
	for i := range s.Agents {
		if s.Agents[i].NostrPubkey == pub {
			return &s.Agents[i]
		}
	}
	return nil
}

func (s *Store) Remove(name string) bool {
	for i, a := range s.Agents {
		if a.Name == name {
			s.Agents = append(s.Agents[:i], s.Agents[i+1:]...)
			return true
		}
	}
	return false
}

func (s *Store) Upsert(a Agent) {
	if existing := s.Find(a.Name); existing != nil {
		*existing = a
		return
	}
	s.Agents = append(s.Agents, a)
}

func SaveCardFile(name, body string) (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	agentDir := filepath.Join(d, name)
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(agentDir, "card")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		return "", err
	}
	return p, nil
}

func RemoveAgentDir(name string) error {
	d, err := config.Dir()
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(d, name))
}

func FormatAllowance(a Allowance) string {
	return fmt.Sprintf("€%.2f / %s", float64(a.Amount)/100, a.Interval)
}
