package oauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OpenAI's public Codex OAuth client. Not secret; PKCE secures the public
// client. The subscription backend is the Responses API at the ChatGPT
// backend, not the standard platform API.
const (
	openAIClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAIAuthorizeEP = "https://auth.openai.com/oauth/authorize"
	openAITokenEP     = "https://auth.openai.com/oauth/token"
	openAIRedirectURI = "http://localhost:1455/auth/callback"
	openAIScopes      = "openid profile email offline_access"

	// OpenAICodexBaseURL is the ChatGPT subscription backend, shaped like the
	// Responses API. Used as the ResponsesService base URL for OAuth.
	OpenAICodexBaseURL = "https://chatgpt.com/backend-api/codex"
)

// OpenAIAuthorizeURL builds the browser URL a user visits to authorize Shelley
// against their ChatGPT subscription.
func OpenAIAuthorizeURL(p PKCE, state string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", openAIClientID)
	q.Set("redirect_uri", openAIRedirectURI)
	q.Set("scope", openAIScopes)
	q.Set("code_challenge", p.Challenge)
	q.Set("code_challenge_method", p.Method)
	q.Set("state", state)
	// Codex requests an API-key grant alongside login.
	q.Set("id_token_add_organizations", "true")
	return openAIAuthorizeEP + "?" + q.Encode()
}

// openAITokenResponse is the wire shape, including the id_token used to derive
// the ChatGPT account id.
type openAITokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	IDToken      string `json:"id_token"`
}

// openAIExchange swaps an authorization code for tokens, extracting the
// ChatGPT account id from the id_token.
func openAIExchange(ctx context.Context, c *http.Client, tokenEP, code string, p PKCE) (Token, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"client_id":     openAIClientID,
		"redirect_uri":  openAIRedirectURI,
		"code_verifier": p.Verifier,
	}
	return openAITokenRequest(ctx, c, tokenEP, body, "")
}

// openAIRefresh mints a new token set from a refresh token, preserving the
// known account id (refresh responses may omit the id_token).
func openAIRefresh(ctx context.Context, c *http.Client, tokenEP, refreshToken, knownAccountID string) (Token, error) {
	body := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     openAIClientID,
		"scope":         openAIScopes,
	}
	return openAITokenRequest(ctx, c, tokenEP, body, knownAccountID)
}

func openAITokenRequest(ctx context.Context, c *http.Client, tokenEP string, body map[string]string, knownAccountID string) (Token, error) {
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
		return Token{}, fmt.Errorf("openai token endpoint: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var tr openAITokenResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		return Token{}, fmt.Errorf("openai token decode: %w", err)
	}
	accountID := knownAccountID
	if id := accountIDFromIDToken(tr.IDToken); id != "" {
		accountID = id
	}
	return Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		AccountID:    accountID,
	}, nil
}

// accountIDFromIDToken extracts the chatgpt_account_id claim from an OAuth
// id_token JWT. Returns "" if the token is malformed or the claim is absent.
func accountIDFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Auth.ChatGPTAccountID
}
