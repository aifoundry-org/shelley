package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"
)

// AnthropicLoginFlow drives a single interactive Claude OAuth login: it holds
// the PKCE pair, produces the browser authorize URL, and exchanges the
// returned code for tokens, persisting them to the store.
type AnthropicLoginFlow struct {
	Store *Store
	HTTPC *http.Client

	pkce    PKCE
	state   string
	tokenEP string // overridable for tests
}

// NewAnthropicLoginFlow builds a login flow with a fresh PKCE pair and state.
func NewAnthropicLoginFlow(store *Store, httpc *http.Client) *AnthropicLoginFlow {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	p, err := NewPKCE()
	if err != nil {
		// crypto/rand failure is fatal and unrecoverable.
		panic(fmt.Sprintf("generate PKCE: %v", err))
	}
	return &AnthropicLoginFlow{
		Store:   store,
		HTTPC:   httpc,
		pkce:    p,
		state:   randomState(),
		tokenEP: anthropicTokenEP,
	}
}

func randomState() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("generate state: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// AuthorizeURL returns the URL the user opens in a browser to authorize.
func (f *AnthropicLoginFlow) AuthorizeURL() string {
	return AnthropicAuthorizeURL(f.pkce, f.state)
}

// Complete exchanges the authorization code (the "code#state" string the user
// pastes back) for tokens and persists them.
func (f *AnthropicLoginFlow) Complete(ctx context.Context, codeState string) error {
	tokenEP := f.tokenEP
	if tokenEP == "" {
		tokenEP = anthropicTokenEP
	}
	tok, err := anthropicExchange(ctx, f.HTTPC, tokenEP, codeState, f.pkce)
	if err != nil {
		return err
	}
	return f.Store.Save("anthropic", tok)
}

// OpenAILoginFlow drives a single interactive Codex/ChatGPT OAuth login.
type OpenAILoginFlow struct {
	Store *Store
	HTTPC *http.Client

	pkce    PKCE
	state   string
	tokenEP string // overridable for tests
}

// NewOpenAILoginFlow builds a login flow with a fresh PKCE pair and state.
func NewOpenAILoginFlow(store *Store, httpc *http.Client) *OpenAILoginFlow {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	p, err := NewPKCE()
	if err != nil {
		panic(fmt.Sprintf("generate PKCE: %v", err))
	}
	return &OpenAILoginFlow{
		Store:   store,
		HTTPC:   httpc,
		pkce:    p,
		state:   randomState(),
		tokenEP: openAITokenEP,
	}
}

// AuthorizeURL returns the URL the user opens in a browser to authorize.
func (f *OpenAILoginFlow) AuthorizeURL() string {
	return OpenAIAuthorizeURL(f.pkce, f.state)
}

// Complete exchanges the authorization code for tokens and persists them.
func (f *OpenAILoginFlow) Complete(ctx context.Context, code string) error {
	tokenEP := f.tokenEP
	if tokenEP == "" {
		tokenEP = openAITokenEP
	}
	tok, err := openAIExchange(ctx, f.HTTPC, tokenEP, code, f.pkce)
	if err != nil {
		return err
	}
	return f.Store.Save("openai", tok)
}

// Status returns a human-readable login status for provider given the store.
func Status(store *Store, provider string, now time.Time) string {
	tok, err := store.Load(provider)
	if err != nil {
		return "not logged in"
	}
	if tok.Expired(now, 0) {
		return fmt.Sprintf("logged in but token expired at %s (will refresh on next use)", tok.ExpiresAt.Format(time.RFC3339))
	}
	return fmt.Sprintf("logged in (token valid until %s)", tok.ExpiresAt.Format(time.RFC3339))
}
