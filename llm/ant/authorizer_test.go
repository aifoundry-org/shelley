package ant

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"shelley.exe.dev/llm"
)

type fakeTokenProvider struct {
	tok string
	err error
}

func (f fakeTokenProvider) AccessToken(context.Context) (string, error) {
	return f.tok, f.err
}

func TestAPIKeyAuthSetsXAPIKey(t *testing.T) {
	h := http.Header{}
	auth := APIKeyAuth{Key: "sk-test"}
	if err := auth.SetAuth(context.Background(), h); err != nil {
		t.Fatalf("SetAuth: %v", err)
	}
	if got := h.Get("X-API-Key"); got != "sk-test" {
		t.Errorf("X-API-Key = %q, want sk-test", got)
	}
	if h.Get("Authorization") != "" {
		t.Error("APIKeyAuth must not set Authorization")
	}
}

func TestAPIKeyAuthNoBetaNoIdentity(t *testing.T) {
	auth := APIKeyAuth{Key: "sk-test"}
	if len(auth.BetaHeaders()) != 0 {
		t.Errorf("BetaHeaders = %v, want none", auth.BetaHeaders())
	}
	if auth.RequiresClaudeCodeIdentity() {
		t.Error("APIKeyAuth must not require Claude Code identity")
	}
}

func TestAPIKeyAuthHasCredential(t *testing.T) {
	if !(APIKeyAuth{Key: "x"}).HasCredential() {
		t.Error("non-empty key should report HasCredential true")
	}
	if (APIKeyAuth{Key: ""}).HasCredential() {
		t.Error("empty key should report HasCredential false")
	}
}

func TestOAuthAuthSetsBearerNotXAPIKey(t *testing.T) {
	h := http.Header{}
	auth := OAuthAuth{Tokens: fakeTokenProvider{tok: "acc-tok"}}
	if err := auth.SetAuth(context.Background(), h); err != nil {
		t.Fatalf("SetAuth: %v", err)
	}
	if got := h.Get("Authorization"); got != "Bearer acc-tok" {
		t.Errorf("Authorization = %q, want Bearer acc-tok", got)
	}
	if h.Get("X-API-Key") != "" {
		t.Error("OAuthAuth must not set X-API-Key")
	}
}

func TestOAuthAuthSetsIdentityHeaders(t *testing.T) {
	h := http.Header{}
	auth := OAuthAuth{Tokens: fakeTokenProvider{tok: "t"}}
	if err := auth.SetAuth(context.Background(), h); err != nil {
		t.Fatalf("SetAuth: %v", err)
	}
	if h.Get("X-App") != "cli" {
		t.Errorf("X-App = %q, want cli", h.Get("X-App"))
	}
	if ua := h.Get("User-Agent"); ua == "" {
		t.Error("User-Agent not set")
	}
}

func TestOAuthAuthBetaAndIdentity(t *testing.T) {
	auth := OAuthAuth{Tokens: fakeTokenProvider{tok: "t"}}
	beta := auth.BetaHeaders()
	if !slices.Contains(beta, "oauth-2025-04-20") || !slices.Contains(beta, "claude-code-20250219") {
		t.Errorf("BetaHeaders = %v, want oauth + claude-code values", beta)
	}
	if !auth.RequiresClaudeCodeIdentity() {
		t.Error("OAuthAuth must require Claude Code identity")
	}
	if !auth.HasCredential() {
		t.Error("OAuthAuth with token provider should report HasCredential")
	}
}

func TestOAuthAuthPropagatesTokenError(t *testing.T) {
	auth := OAuthAuth{Tokens: fakeTokenProvider{err: errors.New("refresh boom")}}
	if err := auth.SetAuth(context.Background(), http.Header{}); err == nil {
		t.Fatal("expected SetAuth to propagate token error")
	}
}

func TestClaudeCodeIdentityInjectedWhenRequired(t *testing.T) {
	s := &Service{Auth: OAuthAuth{Tokens: fakeTokenProvider{tok: "t"}}}
	req := s.fromLLMRequest(&llm.Request{
		System: []llm.SystemContent{{Type: "text", Text: "You are a helpful poet."}},
	})
	if len(req.System) < 2 {
		t.Fatalf("want identity prefix prepended, got %d system blocks", len(req.System))
	}
	if req.System[0].Text != claudeCodeIdentity {
		t.Errorf("first system block = %q, want identity prefix", req.System[0].Text)
	}
	if req.System[1].Text != "You are a helpful poet." {
		t.Errorf("original system block not preserved: %q", req.System[1].Text)
	}
}

func TestClaudeCodeIdentityNotInjectedForAPIKey(t *testing.T) {
	s := &Service{Auth: APIKeyAuth{Key: "k"}}
	req := s.fromLLMRequest(&llm.Request{
		System: []llm.SystemContent{{Type: "text", Text: "You are a helpful poet."}},
	})
	if len(req.System) != 1 || req.System[0].Text != "You are a helpful poet." {
		t.Errorf("APIKey path must not inject identity: %+v", req.System)
	}
}

func TestClaudeCodeIdentityNotDuplicated(t *testing.T) {
	s := &Service{Auth: OAuthAuth{Tokens: fakeTokenProvider{tok: "t"}}}
	req := s.fromLLMRequest(&llm.Request{
		System: []llm.SystemContent{{Type: "text", Text: claudeCodeIdentity}},
	})
	if len(req.System) != 1 {
		t.Errorf("identity should not be duplicated, got %d blocks", len(req.System))
	}
}
