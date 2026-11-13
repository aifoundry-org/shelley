package oauth

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Expired reports whether the token is at or past its expiry, accounting for a
// skew window so callers refresh slightly early.
func (t Token) Expired(now time.Time, skew time.Duration) bool {
	return !now.Add(skew).Before(t.ExpiresAt)
}

// DefaultCredentialsPath returns ~/.config/shelley/credentials.json, honoring
// XDG_CONFIG_HOME, matching the convention in client.DefaultSocketPath.
func DefaultCredentialsPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/tmp"
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "shelley", "credentials.json")
}

var ErrNoCredentials = errors.New("no subscription credentials")

var ErrCredentialsChanged = errors.New("subscription credentials changed during refresh")

// Credentials is where OAuth tokens live, keyed by provider name.
// SaveRefreshed must atomically reject stale refresh results after logout or
// another login, and must not claim fleet ownership.
type Credentials interface {
	Load(provider string) (Token, error)
	Save(provider string, tok Token) error
	Delete(provider string) error
	SaveRefreshed(provider string, previous, refreshed Token) error
}
