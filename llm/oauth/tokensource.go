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

// refreshFunc mints a new token from a refresh token. Injected for testing.
type refreshFunc func(ctx context.Context, refreshToken string) (Token, error)

// TokenSource returns a valid access token, refreshing and persisting it when
// the stored token is near expiry. It is safe for concurrent use.
type TokenSource struct {
	Provider string
	Store    *Store

	now     func() time.Time // defaults to time.Now
	refresh refreshFunc      // defaults to the provider's refresh endpoint

	mu sync.Mutex
}

// NewAnthropicTokenSource builds a TokenSource backed by the Anthropic OAuth
// refresh endpoint and the given credential store.
func NewAnthropicTokenSource(store *Store, httpc *http.Client) *TokenSource {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &TokenSource{
		Provider: "anthropic",
		Store:    store,
		refresh: func(ctx context.Context, rt string) (Token, error) {
			return anthropicRefresh(ctx, httpc, anthropicTokenEP, rt)
		},
	}
}

// NewOpenAITokenSource builds a TokenSource backed by the OpenAI/Codex OAuth
// refresh endpoint and the given credential store.
func NewOpenAITokenSource(store *Store, httpc *http.Client) *TokenSource {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &TokenSource{
		Provider: "openai",
		Store:    store,
		refresh: func(ctx context.Context, rt string) (Token, error) {
			prior, _ := store.Load("openai")
			return openAIRefresh(ctx, httpc, openAITokenEP, rt, prior.AccountID)
		},
	}
}

// AccountID returns the stored ChatGPT account id (OpenAI subscription only).
func (s *TokenSource) AccountID() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, err := s.Store.Load(s.Provider)
	if err != nil {
		return "", err
	}
	return tok.AccountID, nil
}

// AccessToken returns a currently-valid access token, refreshing if needed.
func (s *TokenSource) AccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now
	if s.now != nil {
		now = s.now
	}

	tok, err := s.Store.Load(s.Provider)
	if err != nil {
		return "", err
	}
	if !tok.Expired(now(), refreshSkew) {
		return tok.AccessToken, nil
	}
	if tok.RefreshToken == "" {
		return "", fmt.Errorf("%s token expired and no refresh token available; re-run login", s.Provider)
	}
	refreshed, err := s.refresh(ctx, tok.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("refresh %s token: %w", s.Provider, err)
	}
	if err := s.Store.Save(s.Provider, refreshed); err != nil {
		return "", fmt.Errorf("persist refreshed %s token: %w", s.Provider, err)
	}
	return refreshed.AccessToken, nil
}
