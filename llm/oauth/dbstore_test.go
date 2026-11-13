package oauth

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

func testDBStore(t *testing.T) *DBStore {
	t.Helper()
	return openDBStore(t, filepath.Join(t.TempDir(), "shelley.db"))
}

func openDBStore(t *testing.T, path string) *DBStore {
	t.Helper()
	database, err := db.New(db.Config{DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &DBStore{DB: database}
}

func TestDBStorePersistence(t *testing.T) {
	store := testDBStore(t)
	token := Token{
		AccessToken: "test-access", RefreshToken: "test-refresh",
		ExpiresAt: time.Now().UTC().Truncate(time.Second), AccountID: "test-account",
	}
	if _, err := store.Load("openai"); !errors.Is(err, ErrNoCredentials) {
		t.Fatal("missing token did not return ErrNoCredentials")
	}
	if err := store.Save("openai", token); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("kimi", token); err != nil {
		t.Fatal(err)
	}
	path := store.DB.Path()
	if err := store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	store = openDBStore(t, path)
	if got, err := store.Load("openai"); err != nil || got != token {
		t.Fatal("full token did not survive restart", err)
	}
	if err := store.Delete("openai"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("openai"); err != nil {
		t.Fatal("logout is not idempotent", err)
	}
	if _, err := store.Load("openai"); !errors.Is(err, ErrNoCredentials) {
		t.Fatal("logged-out token did not return ErrNoCredentials")
	}
	if _, err := store.Load("kimi"); err != nil {
		t.Fatal("logout changed a different provider", err)
	}
}

func TestDBStoreConcurrentRefresh(t *testing.T) {
	store := testDBStore(t)
	initial := Token{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour)}
	if err := store.Save("anthropic", initial); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	source := &TokenSource{Provider: "anthropic", Store: store, refresh: func(context.Context, Token) (Token, error) {
		calls.Add(1)
		return Token{AccessToken: "new", RefreshToken: "rotated", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if token, err := source.AccessToken(context.Background()); err != nil || token != "new" {
				t.Error("concurrent refresh failed", err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
}

func TestDBStoreRefreshCannotUndoLogoutOrLogin(t *testing.T) {
	for _, action := range []string{"logout", "login"} {
		t.Run(action, func(t *testing.T) {
			store := testDBStore(t)
			initial := Token{AccessToken: "old", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Hour)}
			if err := store.Save("anthropic", initial); err != nil {
				t.Fatal(err)
			}
			started, finish := make(chan struct{}), make(chan struct{})
			source := &TokenSource{Provider: "anthropic", Store: store, refresh: func(context.Context, Token) (Token, error) {
				close(started)
				<-finish
				return Token{AccessToken: "stale-refresh", RefreshToken: "stale-family"}, nil
			}}
			result := make(chan error, 1)
			go func() { _, err := source.AccessToken(context.Background()); result <- err }()
			<-started
			login := Token{AccessToken: "new-login", RefreshToken: "new-family", ExpiresAt: time.Now().UTC().Add(time.Hour)}
			if action == "logout" {
				if err := store.Delete("anthropic"); err != nil {
					t.Fatal(err)
				}
			} else if err := store.Save("anthropic", login); err != nil {
				t.Fatal(err)
			}
			close(finish)
			if err := <-result; !errors.Is(err, ErrCredentialsChanged) {
				t.Fatal("stale refresh was not rejected", err)
			}
			got, err := store.Load("anthropic")
			if action == "logout" && err == nil {
				t.Fatal("refresh resurrected logged-out credentials")
			}
			if action == "login" && (err != nil || got != login) {
				t.Fatal("refresh overwrote new login", err)
			}
		})
	}
}
