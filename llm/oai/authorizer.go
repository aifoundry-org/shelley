package oai

import (
	"context"
	"net/http"
)

// Authorizer applies authentication to an OpenAI Responses API request and
// declares auth-mode-specific request requirements. Implementations may
// refresh credentials per call, so SetAuth takes a context and may fail.
type Authorizer interface {
	// SetAuth sets the credential header(s) on h for one request attempt.
	SetAuth(ctx context.Context, h http.Header) error
	// RequiresCodexIdentity reports whether requests must carry the Codex
	// system identity prefix (required by ChatGPT subscription OAuth).
	RequiresCodexIdentity() bool
	// HasCredential reports whether a credential is configured (for logging).
	HasCredential() bool
}

// APIKeyAuth authenticates with a standard OpenAI API key via a bearer token.
type APIKeyAuth struct {
	Key string
}

var _ Authorizer = APIKeyAuth{}

func (a APIKeyAuth) SetAuth(_ context.Context, h http.Header) error {
	h.Set("Authorization", "Bearer "+a.Key)
	return nil
}

func (a APIKeyAuth) RequiresCodexIdentity() bool { return false }
func (a APIKeyAuth) HasCredential() bool         { return a.Key != "" }

// codexIdentity is the instruction prefix OpenAI requires on requests made
// with ChatGPT subscription OAuth credentials. The subscription backend
// rejects requests whose first instruction is not the Codex identity.
const codexIdentity = "You are Codex, based on GPT-5. You are running as a coding agent in the Codex CLI on a user's computer."

// TokenProvider yields a currently-valid OAuth access token and the ChatGPT
// account id, refreshing as needed. Implemented by oauth.TokenSource.
type TokenProvider interface {
	AccessToken(ctx context.Context) (string, error)
	AccountID() (string, error)
}

// OAuthAuth authenticates with a ChatGPT subscription via an OAuth bearer
// token plus the chatgpt-account-id header the Codex backend requires.
type OAuthAuth struct {
	Tokens TokenProvider
}

var _ Authorizer = OAuthAuth{}

func (a OAuthAuth) SetAuth(ctx context.Context, h http.Header) error {
	tok, err := a.Tokens.AccessToken(ctx)
	if err != nil {
		return err
	}
	h.Set("Authorization", "Bearer "+tok)
	accountID, err := a.Tokens.AccountID()
	if err != nil {
		return err
	}
	if accountID != "" {
		h.Set("Chatgpt-Account-Id", accountID)
	}
	return nil
}

func (a OAuthAuth) RequiresCodexIdentity() bool { return true }
func (a OAuthAuth) HasCredential() bool         { return a.Tokens != nil }
