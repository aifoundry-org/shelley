package oauth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestSource(t *testing.T, initial Token, now time.Time, refresh refreshFunc) *TokenSource {
	t.Helper()
	store := &Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
	if err := store.Save("anthropic", initial); err != nil {
		t.Fatal(err)
	}
	return &TokenSource{
		Provider: "anthropic",
		Store:    store,
		now:      func() time.Time { return now },
		refresh:  refresh,
	}
}

func TestTokenSourceReturnsValidToken(t *testing.T) {
	now := time.Unix(1000, 0)
	tok := Token{AccessToken: "valid", RefreshToken: "r", ExpiresAt: now.Add(time.Hour)}
	called := false
	src := newTestSource(t, tok, now, func(context.Context, string) (Token, error) {
		called = true
		return Token{}, errors.New("should not refresh")
	})
	got, err := src.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "valid" {
		t.Errorf("token = %q, want valid", got)
	}
	if called {
		t.Error("refresh was called for a still-valid token")
	}
}

func TestTokenSourceRefreshesExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	old := Token{AccessToken: "old", RefreshToken: "oldref", ExpiresAt: now.Add(10 * time.Second)}
	var gotRefreshTok string
	src := newTestSource(t, old, now, func(_ context.Context, rt string) (Token, error) {
		gotRefreshTok = rt
		return Token{AccessToken: "new", RefreshToken: "newref", ExpiresAt: now.Add(time.Hour)}, nil
	})
	got, err := src.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "new" {
		t.Errorf("token = %q, want new", got)
	}
	if gotRefreshTok != "oldref" {
		t.Errorf("refresh called with %q, want oldref", gotRefreshTok)
	}
	// New token must be persisted to the store.
	persisted, err := src.Store.Load("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.AccessToken != "new" || persisted.RefreshToken != "newref" {
		t.Errorf("persisted = %+v, want new/newref", persisted)
	}
}

func TestTokenSourceRefreshError(t *testing.T) {
	now := time.Unix(1000, 0)
	old := Token{AccessToken: "old", RefreshToken: "oldref", ExpiresAt: now.Add(-time.Hour)}
	src := newTestSource(t, old, now, func(context.Context, string) (Token, error) {
		return Token{}, errors.New("refresh failed")
	})
	if _, err := src.AccessToken(context.Background()); err == nil {
		t.Fatal("expected error when refresh fails")
	}
}

func TestTokenSourceAccountID(t *testing.T) {
	now := time.Unix(1000, 0)
	tok := Token{AccessToken: "a", ExpiresAt: now.Add(time.Hour), AccountID: "acct-9"}
	src := newTestSource(t, tok, now, nil)
	got, err := src.AccountID()
	if err != nil {
		t.Fatalf("AccountID: %v", err)
	}
	if got != "acct-9" {
		t.Errorf("AccountID = %q, want acct-9", got)
	}
}
