package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// This JSON store exists only to exercise old-format fixtures and OAuth flows.
// Production subscription credentials use DBStore exclusively.
// Store is an on-disk set of OAuth tokens keyed by provider name. The file is
// a JSON object {provider: Token}, written with 0600 permissions.
type Store struct {
	Path string
	mu   sync.Mutex
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
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readAll()
	if err != nil {
		if os.IsNotExist(err) {
			return Token{}, fmt.Errorf("%w: %s", ErrNoCredentials, provider)
		}
		return Token{}, err
	}
	tok, ok := m[provider]
	if !ok {
		return Token{}, fmt.Errorf("%w: %s", ErrNoCredentials, provider)
	}
	return tok, nil
}

// Save writes the token for provider, preserving other providers' tokens.
func (s *Store) Save(provider string, tok Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
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

// SaveRefreshed exists for legacy store fixtures; production uses DBStore.
func (s *Store) SaveRefreshed(provider string, previous, refreshed Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readAll()
	if err != nil {
		if os.IsNotExist(err) {
			return ErrCredentialsChanged
		}
		return err
	}
	current, ok := m[provider]
	if !ok || current.AccessToken != previous.AccessToken || current.RefreshToken != previous.RefreshToken ||
		current.AccountID != previous.AccountID || !current.ExpiresAt.Equal(previous.ExpiresAt) {
		return ErrCredentialsChanged
	}
	m[provider] = refreshed
	return s.writeAll(m)
}
