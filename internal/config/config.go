package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	StripeAPIKey  string   `json:"stripe_api_key"`
	Billing       *Billing `json:"billing,omitempty"`
	WebhookSecret string   `json:"webhook_secret,omitempty"`
	WebhookURL    string   `json:"webhook_url,omitempty"`
	DaemonPort    int      `json:"daemon_port,omitempty"`

	// AdminNostrPubkey (hex) is DM'd when an issuing_authorization.request
	// arrives without a matching auto-approve rule. A reply of "approve" (or
	// "yes", "ok", "👍") from this pubkey triggers approval.
	AdminNostrPubkey string `json:"admin_nostr_pubkey,omitempty"`
	// AdminNIP05 is the human-readable identifier the admin gave during setup.
	// Kept alongside the hex pubkey purely for display.
	AdminNIP05 string `json:"admin_nip05,omitempty"`
}

// Billing holds the default cardholder identity and address used when creating
// new agents. These fields satisfy Stripe Issuing KYC requirements.
type Billing struct {
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	DOB         string `json:"dob"` // YYYY-MM-DD
	Email       string `json:"email,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Line1       string `json:"line1"`
	Line2       string `json:"line2,omitempty"`
	City        string `json:"city"`
	State       string `json:"state,omitempty"`
	PostalCode  string `json:"postal_code"`
	Country     string `json:"country"` // ISO 3166-1 alpha-2
}

// Complete reports whether all required cardholder fields are set.
func (b *Billing) Complete() bool {
	if b == nil {
		return false
	}
	_, _, _, ok := ParseDOB(b.DOB)
	return b.FirstName != "" && b.LastName != "" && ok &&
		b.Line1 != "" && b.City != "" && b.PostalCode != "" && b.Country != ""
}

// ParseDOB parses a YYYY-MM-DD string into day, month, year integers.
func ParseDOB(s string) (day, month, year int, ok bool) {
	parts := strings.Split(strings.TrimSpace(s), "-")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	y, errY := strconv.Atoi(parts[0])
	m, errM := strconv.Atoi(parts[1])
	d, errD := strconv.Atoi(parts[2])
	if errY != nil || errM != nil || errD != nil {
		return 0, 0, 0, false
	}
	if y < 1900 || y > 2100 || m < 1 || m > 12 || d < 1 || d > 31 {
		return 0, 0, 0, false
	}
	return d, m, y, true
}

var ErrNotConfigured = errors.New("agentdesk not set up — run `agentdesk setup`")

func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agentdesk"), nil
}

func path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// Load returns the config. Returns ErrNotConfigured if the file or key is missing.
func Load() (*Config, error) {
	c, err := LoadOrEmpty()
	if err != nil {
		return nil, err
	}
	if c.StripeAPIKey == "" {
		return nil, ErrNotConfigured
	}
	return c, nil
}

// LoadOrEmpty returns the config, or an empty one if no file exists.
// Useful for `setup` which needs to inspect the current state.
func LoadOrEmpty() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func Save(c *Config) error {
	d, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(d, "config.json")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Saved config to %s\n", p)
	return nil
}
