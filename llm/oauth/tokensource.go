package oauth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// refreshSkew is how long before expiry a token is considered stale, so we
// refresh slightly early rather than racing the deadline.
const refreshSkew = 60 * time.Second

// refreshFunc mints a new token from the exact stored snapshot. Injected for testing.
type refreshFunc func(ctx context.Context, tok Token) (Token, error)

// TokenSource returns a valid access token, refreshing and persisting it when
// the stored token is near expiry. It is safe for concurrent use.
type TokenSource struct {
	Provider string
	Store    Credentials

	now     func() time.Time // defaults to time.Now
	refresh refreshFunc      // defaults to the provider's refresh endpoint

	mu sync.Mutex
}

// NewAnthropicTokenSource builds a TokenSource backed by the Anthropic OAuth
// refresh endpoint and the given credential store.
func NewAnthropicTokenSource(store Credentials, httpc *http.Client) *TokenSource {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &TokenSource{
		Provider: "anthropic",
		Store:    store,
		refresh: func(ctx context.Context, tok Token) (Token, error) {
			return anthropicRefresh(ctx, httpc, anthropicTokenEP, tok.RefreshToken)
		},
	}
}

// NewOpenAITokenSource builds a TokenSource backed by the OpenAI/Codex OAuth
// refresh endpoint and the given credential store.
func NewOpenAITokenSource(store Credentials, httpc *http.Client) *TokenSource {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &TokenSource{
		Provider: "openai",
		Store:    store,
		refresh: func(ctx context.Context, tok Token) (Token, error) {
			return openAIRefresh(ctx, httpc, openAITokenEP, tok.RefreshToken, tok.AccountID)
		},
	}
}

// AccessToken returns a currently-valid access token, refreshing if needed.
func (s *TokenSource) AccessToken(ctx context.Context) (string, error) {
	accessToken, _, err := s.AccessTokenWithAccount(ctx)
	return accessToken, err
}

// AccessTokenWithAccount returns a valid access token and its account id from
// one credential snapshot, refreshing if needed.
func (s *TokenSource) AccessTokenWithAccount(ctx context.Context) (accessToken, accountID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now
	if s.now != nil {
		now = s.now
	}

	tok, err := s.Store.Load(s.Provider)
	if err != nil {
		return "", "", err
	}
	if !tok.Expired(now(), refreshSkew) {
		return tok.AccessToken, tok.AccountID, nil
	}
	refreshed, err := s.refreshLocked(ctx, tok)
	if err != nil {
		return "", "", err
	}
	return refreshed.AccessToken, refreshed.AccountID, nil
}

// Refresh mints and persists a new token now, regardless of expiry.
func (s *TokenSource) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, err := s.Store.Load(s.Provider)
	if err != nil {
		return err
	}
	_, err = s.refreshLocked(ctx, tok)
	return err
}

func (s *TokenSource) refreshLocked(ctx context.Context, tok Token) (Token, error) {
	if tok.RefreshToken == "" {
		return Token{}, fmt.Errorf("%s token expired and no refresh token available; re-run login", s.Provider)
	}
	refreshed, err := s.refresh(ctx, tok)
	if err != nil {
		return Token{}, fmt.Errorf("refresh %s token: %w", s.Provider, err)
	}
	if err := s.Store.SaveRefreshed(s.Provider, tok, refreshed); err != nil {
		return Token{}, fmt.Errorf("persist refreshed %s token: %w", s.Provider, err)
	}
	return refreshed, nil
}
