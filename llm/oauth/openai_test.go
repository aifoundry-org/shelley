package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestOpenAIAuthorizeURL(t *testing.T) {
	p := PKCE{Verifier: "v", Challenge: "chal", Method: "S256"}
	raw := OpenAIAuthorizeURL(p, "state123")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	checks := map[string]string{
		"response_type":         "code",
		"client_id":             openAIClientID,
		"redirect_uri":          openAIRedirectURI,
		"code_challenge":        "chal",
		"code_challenge_method": "S256",
		"state":                 "state123",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("query[%q] = %q, want %q", k, got, want)
		}
	}
	if q.Get("scope") == "" {
		t.Error("scope missing")
	}
}

// makeIDToken builds an unsigned JWT (header.payload.sig) embedding the given
// chatgpt_account_id claim, as returned by the OpenAI token endpoint.
func makeIDToken(accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims := map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	}
	payloadJSON, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	return header + "." + payload + "."
}

func TestOpenAIExchangeExtractsAccountID(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "acc",
			"refresh_token": "ref",
			"expires_in":    3600,
			"id_token":      makeIDToken("acct-123"),
		})
	}))
	defer srv.Close()

	p := PKCE{Verifier: "theverifier"}
	tok, err := openAIExchange(context.Background(), srv.Client(), srv.URL, "thecode", p)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if gotBody["grant_type"] != "authorization_code" {
		t.Errorf("grant_type = %v", gotBody["grant_type"])
	}
	if gotBody["code"] != "thecode" {
		t.Errorf("code = %v", gotBody["code"])
	}
	if gotBody["code_verifier"] != "theverifier" {
		t.Errorf("code_verifier = %v", gotBody["code_verifier"])
	}
	if tok.AccessToken != "acc" || tok.RefreshToken != "ref" {
		t.Errorf("tok = %+v", tok)
	}
	if tok.AccountID != "acct-123" {
		t.Errorf("AccountID = %q, want acct-123", tok.AccountID)
	}
}

func TestOpenAIRefreshPreservesAccountID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Refresh responses often omit id_token; account id should be preserved.
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "acc2",
			"refresh_token": "ref2",
			"expires_in":    7200,
		})
	}))
	defer srv.Close()

	tok, err := openAIRefresh(context.Background(), srv.Client(), srv.URL, "oldref", "acct-existing")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if tok.AccessToken != "acc2" {
		t.Errorf("AccessToken = %q", tok.AccessToken)
	}
	if tok.AccountID != "acct-existing" {
		t.Errorf("AccountID = %q, want preserved acct-existing", tok.AccountID)
	}
}

func TestAccountIDFromIDToken(t *testing.T) {
	if got := accountIDFromIDToken(makeIDToken("abc")); got != "abc" {
		t.Errorf("accountIDFromIDToken = %q, want abc", got)
	}
	if got := accountIDFromIDToken(""); got != "" {
		t.Errorf("empty token = %q, want empty", got)
	}
	if got := accountIDFromIDToken("not.a.jwt!!"); got != "" {
		t.Errorf("garbage token = %q, want empty", got)
	}
}
