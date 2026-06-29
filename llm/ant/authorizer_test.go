package ant

import (
	"context"
	"net/http"
	"testing"
)

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
