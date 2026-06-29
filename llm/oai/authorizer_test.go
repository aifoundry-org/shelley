package oai

import (
	"context"
	"net/http"
	"testing"
)

func TestAPIKeyAuthSetsBearer(t *testing.T) {
	h := http.Header{}
	auth := APIKeyAuth{Key: "sk-test"}
	if err := auth.SetAuth(context.Background(), h); err != nil {
		t.Fatalf("SetAuth: %v", err)
	}
	if got := h.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", got)
	}
}

func TestAPIKeyAuthNoIdentity(t *testing.T) {
	auth := APIKeyAuth{Key: "sk-test"}
	if auth.RequiresCodexIdentity() {
		t.Error("APIKeyAuth must not require Codex identity")
	}
	if !auth.HasCredential() {
		t.Error("non-empty key should report HasCredential")
	}
	if (APIKeyAuth{Key: ""}).HasCredential() {
		t.Error("empty key should report HasCredential false")
	}
}
