// Package nostrd runs the agentdesk Nostr listener. Agents request virtual
// cards by sending NIP-04 encrypted direct messages to the daemon's npub; the
// daemon responds with card details in another encrypted DM.
package nostrd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"

	"github.com/xdamman/agentdesk/internal/config"
)

// Identity holds the daemon's Nostr keypair in multiple encodings.
type Identity struct {
	SecretHex string
	PubHex    string
	Nsec      string
	Npub      string
}

func keyPath() (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "nsec"), nil
}

// LoadOrGenerate returns the daemon's Nostr identity, creating a new keypair
// saved to ~/.agentdesk/nsec (0600) on first run. The file stores a single line
// in either hex or bech32 nsec format.
func LoadOrGenerate() (*Identity, error) {
	p, err := keyPath()
	if err != nil {
		return nil, err
	}
	var sk string
	if b, rErr := os.ReadFile(p); rErr == nil {
		sk = strings.TrimSpace(string(b))
		if strings.HasPrefix(sk, "nsec") {
			prefix, decoded, dErr := nip19.Decode(sk)
			if dErr != nil || prefix != "nsec" {
				return nil, fmt.Errorf("decode nsec in %s: %w", p, dErr)
			}
			hex, ok := decoded.(string)
			if !ok {
				return nil, fmt.Errorf("unexpected nsec decoded type %T", decoded)
			}
			sk = hex
		}
	} else if !os.IsNotExist(rErr) {
		return nil, rErr
	}

	if sk == "" {
		sk = nostr.GeneratePrivateKey()
		dir, _ := config.Dir()
		if mErr := os.MkdirAll(dir, 0o700); mErr != nil {
			return nil, mErr
		}
		if wErr := os.WriteFile(p, []byte(sk+"\n"), 0o600); wErr != nil {
			return nil, wErr
		}
	}

	pub, err := nostr.GetPublicKey(sk)
	if err != nil {
		return nil, err
	}
	nsec, err := nip19.EncodePrivateKey(sk)
	if err != nil {
		return nil, err
	}
	npub, err := nip19.EncodePublicKey(pub)
	if err != nil {
		return nil, err
	}
	return &Identity{SecretHex: sk, PubHex: pub, Nsec: nsec, Npub: npub}, nil
}
