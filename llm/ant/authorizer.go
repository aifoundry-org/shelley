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

// claudeCodeIdentity is the system prefix Anthropic requires on requests made
// with subscription OAuth credentials. Without it the API rejects the request.
const claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

// claudeCodeUserAgent identifies the request as the Claude Code CLI, as the
// subscription OAuth backend expects.
const claudeCodeUserAgent = "claude-cli/2.0.0 (external, cli)"

// TokenProvider yields a currently-valid OAuth access token, refreshing as
// needed. Implemented by oauth.TokenSource.
type TokenProvider interface {
	AccessToken(ctx context.Context) (string, error)
}

// OAuthAuth authenticates with a Claude subscription via an OAuth bearer token
// plus the Claude Code identity headers the subscription backend requires.
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
	h.Set("User-Agent", claudeCodeUserAgent)
	h.Set("X-App", "cli")
	return nil
}

func (a OAuthAuth) BetaHeaders() []string {
	return []string{"oauth-2025-04-20", "claude-code-20250219"}
}

func (a OAuthAuth) RequiresClaudeCodeIdentity() bool { return true }
func (a OAuthAuth) HasCredential() bool              { return a.Tokens != nil }

// KimiOAuthAuth authenticates Kimi Code subscription requests. Unlike Claude
// subscription OAuth it needs only a fresh bearer token, not CLI impersonation.
type KimiOAuthAuth struct {
	Tokens TokenProvider
}

var _ Authorizer = KimiOAuthAuth{}

func (a KimiOAuthAuth) SetAuth(ctx context.Context, h http.Header) error {
	tok, err := a.Tokens.AccessToken(ctx)
	if err != nil {
		return err
	}
	h.Set("Authorization", "Bearer "+tok)
	return nil
}

func (a KimiOAuthAuth) BetaHeaders() []string            { return nil }
func (a KimiOAuthAuth) RequiresClaudeCodeIdentity() bool { return false }
func (a KimiOAuthAuth) HasCredential() bool              { return a.Tokens != nil }
