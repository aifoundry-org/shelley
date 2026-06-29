package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnthropicLoginPersistsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "acc",
			"refresh_token": "ref",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	store := &Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
	flow := &AnthropicLoginFlow{
		Store:   store,
		HTTPC:   srv.Client(),
		tokenEP: srv.URL,
	}

	// PromptURL should be a valid authorize URL.
	if !strings.HasPrefix(flow.AuthorizeURL(), anthropicAuthorizeEP) {
		t.Errorf("AuthorizeURL = %q", flow.AuthorizeURL())
	}

	if err := flow.Complete(context.Background(), "thecode#thestate"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	tok, err := store.Load("anthropic")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if tok.AccessToken != "acc" || tok.RefreshToken != "ref" {
		t.Errorf("stored token = %+v", tok)
	}
}

func TestNewAnthropicLoginFlowHasPKCE(t *testing.T) {
	flow := NewAnthropicLoginFlow(&Store{Path: "x"}, nil)
	if flow.pkce.Verifier == "" || flow.pkce.Challenge == "" {
		t.Error("login flow missing PKCE pair")
	}
	if len(flow.state) != 43 {
		t.Errorf("state length = %d, want 43", len(flow.state))
	}
	if !strings.Contains(flow.AuthorizeURL(), flow.pkce.Challenge) {
		t.Error("authorize URL does not embed PKCE challenge")
	}
}

func TestStatusReportsLoggedIn(t *testing.T) {
	store := &Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
	if Status(store, "anthropic", time.Now()) != "not logged in" {
		t.Error("expected not logged in for empty store")
	}
	store.Save("anthropic", Token{AccessToken: "a", ExpiresAt: time.Unix(2000, 0)})
	if got := Status(store, "anthropic", time.Unix(1000, 0)); !strings.Contains(got, "logged in") {
		t.Errorf("Status = %q, want logged in", got)
	}
	if got := Status(store, "anthropic", time.Unix(3000, 0)); !strings.Contains(got, "expired") {
		t.Errorf("Status = %q, want expired", got)
	}
}

func TestOpenAILoginPersistsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "acc",
			"refresh_token": "ref",
			"expires_in":    3600,
			"id_token":      makeIDToken("acct-7"),
		})
	}))
	defer srv.Close()

	store := &Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
	flow := NewOpenAILoginFlow(store, srv.Client())
	flow.tokenEP = srv.URL

	if !strings.HasPrefix(flow.AuthorizeURL(), openAIAuthorizeEP) {
		t.Errorf("AuthorizeURL = %q", flow.AuthorizeURL())
	}
	if err := flow.Complete(context.Background(), "thecode"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	tok, err := store.Load("openai")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if tok.AccessToken != "acc" || tok.AccountID != "acct-7" {
		t.Errorf("stored token = %+v", tok)
	}
}
