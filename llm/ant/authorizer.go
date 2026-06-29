package ant

import (
	"context"
	"net/http"
)

// Authorizer applies authentication to an Anthropic Messages API request and
// declares any auth-mode-specific request requirements. Implementations may
// refresh credentials on each call, so SetAuth takes a context and may fail.
type Authorizer interface {
	// SetAuth sets the credential header(s) on h for one request attempt.
	SetAuth(ctx context.Context, h http.Header) error
	// BetaHeaders returns extra values to merge into the anthropic-beta header.
	BetaHeaders() []string
	// RequiresClaudeCodeIdentity reports whether requests must carry the
	// Claude Code system identity prefix (required by subscription OAuth).
	RequiresClaudeCodeIdentity() bool
	// HasCredential reports whether a credential is configured (for logging).
	HasCredential() bool
}

// APIKeyAuth authenticates with a standard Anthropic API key via X-API-Key.
type APIKeyAuth struct {
	Key string
}

var _ Authorizer = APIKeyAuth{}

func (a APIKeyAuth) SetAuth(_ context.Context, h http.Header) error {
	h.Set("X-API-Key", a.Key)
	return nil
}

func (a APIKeyAuth) BetaHeaders() []string            { return nil }
func (a APIKeyAuth) RequiresClaudeCodeIdentity() bool { return false }
func (a APIKeyAuth) HasCredential() bool              { return a.Key != "" }
