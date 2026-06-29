package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestAnthropicAuthorizeURL(t *testing.T) {
	p := PKCE{Verifier: "v", Challenge: "chal", Method: "S256"}
	raw := AnthropicAuthorizeURL(p, "state123")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	checks := map[string]string{
		"response_type":         "code",
		"client_id":             anthropicClientID,
		"redirect_uri":          anthropicRedirectURI,
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

func TestAnthropicExchangeCode(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "acc",
			"refresh_token": "ref",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	p := PKCE{Verifier: "theverifier"}
	// Anthropic returns the auth code as "code#state"; we split on '#'.
	tok, err := anthropicExchange(context.Background(), srv.Client(), srv.URL, "thecode#thestate", p)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if gotBody["grant_type"] != "authorization_code" {
		t.Errorf("grant_type = %v", gotBody["grant_type"])
	}
	if gotBody["code"] != "thecode" {
		t.Errorf("code = %v, want thecode", gotBody["code"])
	}
	if gotBody["state"] != "thestate" {
		t.Errorf("state = %v, want thestate", gotBody["state"])
	}
	if gotBody["code_verifier"] != "theverifier" {
		t.Errorf("code_verifier = %v", gotBody["code_verifier"])
	}
	if gotBody["client_id"] != anthropicClientID {
		t.Errorf("client_id = %v", gotBody["client_id"])
	}
	if tok.AccessToken != "acc" || tok.RefreshToken != "ref" {
		t.Errorf("tok = %+v", tok)
	}
	if tok.ExpiresAt.IsZero() {
		t.Error("ExpiresAt not set from expires_in")
	}
}

func TestAnthropicRefresh(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "acc2",
			"refresh_token": "ref2",
			"expires_in":    7200,
		})
	}))
	defer srv.Close()

	tok, err := anthropicRefresh(context.Background(), srv.Client(), srv.URL, "oldref")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if gotBody["grant_type"] != "refresh_token" {
		t.Errorf("grant_type = %v", gotBody["grant_type"])
	}
	if gotBody["refresh_token"] != "oldref" {
		t.Errorf("refresh_token = %v", gotBody["refresh_token"])
	}
	if tok.AccessToken != "acc2" || tok.RefreshToken != "ref2" {
		t.Errorf("tok = %+v", tok)
	}
}

func TestAnthropicExchangeHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := anthropicExchange(context.Background(), srv.Client(), srv.URL, "c#s", PKCE{})
	if err == nil {
		t.Fatal("expected error on 400")
	}
}
