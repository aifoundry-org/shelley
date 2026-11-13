package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
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
	src := newTestSource(t, tok, now, func(context.Context, Token) (Token, error) {
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
	now := time.Unix(1000, 0).UTC()
	old := Token{AccessToken: "old", RefreshToken: "oldref", ExpiresAt: now.Add(10 * time.Second)}
	var gotRefreshTok Token
	src := newTestSource(t, old, now, func(_ context.Context, tok Token) (Token, error) {
		gotRefreshTok = tok
		return Token{AccessToken: "new", RefreshToken: "newref", ExpiresAt: now.Add(time.Hour)}, nil
	})
	got, err := src.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "new" {
		t.Errorf("token = %q, want new", got)
	}
	if gotRefreshTok != old {
		t.Error("refresh did not receive the exact stored snapshot")
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
	src := newTestSource(t, old, now, func(context.Context, Token) (Token, error) {
		return Token{}, errors.New("refresh failed")
	})
	if _, err := src.AccessToken(context.Background()); err == nil {
		t.Fatal("expected error when refresh fails")
	}
}

func TestTokenSourceAccessTokenWithAccount(t *testing.T) {
	now := time.Unix(1000, 0)
	store := &switchingCredentials{
		first:  Token{AccessToken: "a", ExpiresAt: now.Add(time.Hour), AccountID: "acct-a"},
		second: Token{AccessToken: "b", ExpiresAt: now.Add(time.Hour), AccountID: "acct-b"},
	}
	src := NewOpenAITokenSource(store, nil)
	src.now = func() time.Time { return now }
	for _, want := range []Token{store.first, store.second} {
		access, account, err := src.AccessTokenWithAccount(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if access != want.AccessToken || account != want.AccountID {
			t.Error("access token and account must come from the same credential snapshot")
		}
	}
	if store.loads != 2 {
		t.Fatalf("Load calls = %d, want 1 per snapshot", store.loads)
	}
}

// A rotating/fleet store can resolve a different owner on its next Load.
type switchingCredentials struct {
	Credentials
	first, second       Token
	loads               int
	previous, refreshed Token
	loadErr, saveErr    error
}

func (s *switchingCredentials) Load(string) (Token, error) {
	s.loads++
	if s.loadErr != nil {
		return Token{}, s.loadErr
	}
	if s.loads == 1 {
		return s.first, nil
	}
	return s.second, nil
}

func (s *switchingCredentials) SaveRefreshed(_ string, previous, refreshed Token) error {
	s.previous, s.refreshed = previous, refreshed
	return s.saveErr
}

func TestOpenAITokenSourceRefreshUsesOneSnapshot(t *testing.T) {
	for _, mode := range []string{"expired", "forced"} {
		t.Run(mode, func(t *testing.T) {
			force := mode == "forced"
			now := time.Unix(1000, 0)
			store := &switchingCredentials{
				first:  Token{AccessToken: "old-a", RefreshToken: "refresh-a", AccountID: "acct-a", ExpiresAt: now.Add(-time.Hour)},
				second: Token{AccessToken: "b", RefreshToken: "refresh-b", AccountID: "acct-b"},
			}
			if force {
				store.first.ExpiresAt = now.Add(time.Hour)
			}
			calls := 0
			client := &http.Client{Transport: kimiTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != openAITokenEP {
					t.Fatal("unexpected OAuth endpoint")
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["refresh_token"] != store.first.RefreshToken {
					t.Error("refresh spent a different snapshot's credential")
				}
				// No id_token: the account must be preserved from the spent token.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"new-a","refresh_token":"rotated-a","expires_in":3600}`)),
				}, nil
			})}
			src := NewOpenAITokenSource(store, client)
			src.now = func() time.Time { return now }
			if force {
				if err := src.Refresh(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				access, account, err := src.AccessTokenWithAccount(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if access != "new-a" || account != "acct-a" {
					t.Error("refreshed access token and account must belong to the same owner")
				}
			}
			if calls != 1 || store.loads != 1 {
				t.Fatalf("refresh calls = %d, Load calls = %d; want 1 each", calls, store.loads)
			}
			if store.previous != store.first {
				t.Error("SaveRefreshed must compare against the spent snapshot")
			}
			if store.refreshed.AccountID != "acct-a" || store.refreshed.AccessToken != "new-a" || store.refreshed.RefreshToken != "rotated-a" {
				t.Error("persisted refresh combined credentials from different owners")
			}
		})
	}
}

func TestTokenSourceSnapshotErrors(t *testing.T) {
	failure := errors.New("credential operation failed")
	for _, stage := range []string{"load", "refresh", "persist"} {
		t.Run(stage, func(t *testing.T) {
			store := &switchingCredentials{first: Token{RefreshToken: "refresh", AccountID: "account"}}
			src := NewOpenAITokenSource(store, nil)
			src.refresh = func(context.Context, Token) (Token, error) {
				if stage == "refresh" {
					return Token{}, failure
				}
				return Token{AccessToken: "new", AccountID: "account"}, nil
			}
			switch stage {
			case "load":
				store.loadErr = failure
			case "persist":
				store.saveErr = ErrCredentialsChanged
			}
			want := failure
			if stage == "persist" {
				want = ErrCredentialsChanged
			}
			access, account, err := src.AccessTokenWithAccount(context.Background())
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if access != "" || account != "" {
				t.Error("failed token retrieval must not expose credentials")
			}
		})
	}
}
