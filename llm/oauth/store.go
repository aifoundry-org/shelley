package oauth

import (
	"encoding/json"
	"fmt"
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

// Store is an on-disk set of OAuth tokens keyed by provider name. The file is
// a JSON object {provider: Token}, written with 0600 permissions.
type Store struct {
	Path string
}

func (s *Store) readAll() (map[string]Token, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, err
	}
	m := map[string]Token{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse credentials %s: %w", s.Path, err)
	}
	return m, nil
}

func (s *Store) writeAll(m map[string]Token) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, data, 0o600)
}

// Load returns the token for provider, or an error if absent.
func (s *Store) Load(provider string) (Token, error) {
	m, err := s.readAll()
	if err != nil {
		return Token{}, err
	}
	tok, ok := m[provider]
	if !ok {
		return Token{}, fmt.Errorf("no %s credentials in %s", provider, s.Path)
	}
	return tok, nil
}

// Save writes the token for provider, preserving other providers' tokens.
func (s *Store) Save(provider string, tok Token) error {
	m, err := s.readAll()
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		m = map[string]Token{}
	}
	m[provider] = tok
	return s.writeAll(m)
}

// Delete removes the token for provider, preserving others. It is a no-op if
// the file or provider entry does not exist.
func (s *Store) Delete(provider string) error {
	m, err := s.readAll()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	delete(m, provider)
	return s.writeAll(m)
}
