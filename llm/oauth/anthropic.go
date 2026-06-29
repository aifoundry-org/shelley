package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Anthropic's public Claude Code OAuth client. These values are not secret;
// the client is "public" in OAuth terms and relies on PKCE for security.
const (
	anthropicClientID    = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	anthropicAuthorizeEP = "https://claude.ai/oauth/authorize"
	anthropicTokenEP     = "https://console.anthropic.com/v1/oauth/token"
	anthropicRedirectURI = "https://console.anthropic.com/oauth/code/callback"
	anthropicScopes      = "org:create_api_key user:profile user:inference"
)

// Token is an OAuth credential set. ExpiresAt is absolute wall-clock time.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	// AccountID is the ChatGPT account id (OpenAI subscription only), extracted
	// from the id_token JWT and sent as the chatgpt-account-id header.
	AccountID string `json:"account_id,omitempty"`
}

// AnthropicAuthorizeURL builds the browser URL a user visits to authorize
// Shelley against their Claude subscription.
func AnthropicAuthorizeURL(p PKCE, state string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", anthropicClientID)
	q.Set("redirect_uri", anthropicRedirectURI)
	q.Set("scope", anthropicScopes)
	q.Set("code_challenge", p.Challenge)
	q.Set("code_challenge_method", p.Method)
	q.Set("state", state)
	return anthropicAuthorizeEP + "?" + q.Encode()
}

// tokenResponse is the wire shape of an OAuth token endpoint reply.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func (tr tokenResponse) toToken(now time.Time) Token {
	return Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(tr.ExpiresIn) * time.Second),
	}
}

// anthropicExchange swaps an authorization code ("code#state") for tokens.
func anthropicExchange(ctx context.Context, c *http.Client, tokenEP, codeState string, p PKCE) (Token, error) {
	code, state, _ := strings.Cut(codeState, "#")
	body := map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"state":         state,
		"client_id":     anthropicClientID,
		"redirect_uri":  anthropicRedirectURI,
		"code_verifier": p.Verifier,
	}
	return anthropicTokenRequest(ctx, c, tokenEP, body)
}

// anthropicRefresh mints a new token set from a refresh token.
func anthropicRefresh(ctx context.Context, c *http.Client, tokenEP, refreshToken string) (Token, error) {
	body := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     anthropicClientID,
	}
	return anthropicTokenRequest(ctx, c, tokenEP, body)
}

func anthropicTokenRequest(ctx context.Context, c *http.Client, tokenEP string, body map[string]string) (Token, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return Token{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", tokenEP, bytes.NewReader(payload))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Token{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("oauth token endpoint: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var tr tokenResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		return Token{}, fmt.Errorf("oauth token decode: %w", err)
	}
	return tr.toToken(time.Now()), nil
}
