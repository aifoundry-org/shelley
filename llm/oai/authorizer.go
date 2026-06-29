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
