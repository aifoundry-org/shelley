package oai

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

type fakeTokenProvider struct {
	tok       string
	accountID string
	err       error
}

func (f fakeTokenProvider) AccessTokenWithAccount(context.Context) (string, string, error) {
	return f.tok, f.accountID, f.err
}

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

func TestOAuthAuthSetsBearerAndAccountID(t *testing.T) {
	h := http.Header{}
	auth := OAuthAuth{Tokens: fakeTokenProvider{tok: "acc-tok", accountID: "acct-1"}}
	if err := auth.SetAuth(context.Background(), h); err != nil {
		t.Fatalf("SetAuth: %v", err)
	}
	if got := h.Get("Authorization"); got != "Bearer acc-tok" {
		t.Errorf("Authorization = %q", got)
	}
	if got := h.Get("Chatgpt-Account-Id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id = %q, want acct-1", got)
	}
}

func TestOAuthAuthOmitsAccountIDWhenEmpty(t *testing.T) {
	h := http.Header{"Chatgpt-Account-Id": {"previous-account"}}
	auth := OAuthAuth{Tokens: fakeTokenProvider{tok: "t"}}
	if err := auth.SetAuth(context.Background(), h); err != nil {
		t.Fatalf("SetAuth: %v", err)
	}
	if _, ok := h["Chatgpt-Account-Id"]; ok {
		t.Error("chatgpt-account-id should be omitted when empty")
	}
}

func TestOAuthAuthRequiresCodexIdentity(t *testing.T) {
	auth := OAuthAuth{Tokens: fakeTokenProvider{tok: "t"}}
	if !auth.RequiresCodexIdentity() {
		t.Error("OAuthAuth must require Codex identity")
	}
	if !auth.HasCredential() {
		t.Error("OAuthAuth with provider should report HasCredential")
	}
}

func TestOAuthAuthPropagatesTokenError(t *testing.T) {
	failure := errors.New("boom")
	auth := OAuthAuth{Tokens: fakeTokenProvider{tok: "unusable", accountID: "unusable", err: failure}}
	h := http.Header{}
	if err := auth.SetAuth(context.Background(), h); !errors.Is(err, failure) {
		t.Fatal("expected token error to propagate")
	}
	if len(h) != 0 {
		t.Error("failed snapshot must not set authentication headers")
	}
}

type rotatingSnapshotProvider struct {
	calls int
	ctx   context.Context
}

func (p *rotatingSnapshotProvider) AccessTokenWithAccount(ctx context.Context) (string, string, error) {
	p.calls++
	p.ctx = ctx
	if p.calls == 1 {
		return "token-a", "account-a", nil
	}
	return "token-b", "account-b", nil
}

func TestOAuthAuthUsesOneSnapshotPerAttempt(t *testing.T) {
	tokens := &rotatingSnapshotProvider{}
	auth := OAuthAuth{Tokens: tokens}
	h := http.Header{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i, want := range []struct{ token, account string }{
		{"token-a", "account-a"},
		{"token-b", "account-b"},
	} {
		if err := auth.SetAuth(ctx, h); err != nil {
			t.Fatal(err)
		}
		if tokens.calls != i+1 {
			t.Fatal("each request attempt must read exactly one credential snapshot")
		}
		if tokens.ctx != ctx {
			t.Error("snapshot did not receive request context")
		}
		if h.Get("Authorization") != "Bearer "+want.token || h.Get("Chatgpt-Account-Id") != want.account {
			t.Error("request combined bearer and account from different snapshots")
		}
	}
}

func TestCodexIdentityPrependedToInstructions(t *testing.T) {
	svc := &ResponsesService{Auth: OAuthAuth{Tokens: fakeTokenProvider{tok: "t"}}}
	instr := svc.instructions([]llm.SystemContent{{Text: "You are a poet."}})
	if !strings.HasPrefix(instr, codexIdentity) {
		t.Errorf("instructions did not start with Codex identity: %q", instr)
	}
	if !strings.Contains(instr, "You are a poet.") {
		t.Error("original instructions not preserved")
	}
}

func TestCodexIdentityNotPrependedForAPIKey(t *testing.T) {
	svc := &ResponsesService{Auth: APIKeyAuth{Key: "k"}}
	instr := svc.instructions([]llm.SystemContent{{Text: "You are a poet."}})
	if instr != "You are a poet." {
		t.Errorf("APIKey path must not inject identity: %q", instr)
	}
}

func TestCodexIdentityNotDuplicated(t *testing.T) {
	svc := &ResponsesService{Auth: OAuthAuth{Tokens: fakeTokenProvider{tok: "t"}}}
	instr := svc.instructions([]llm.SystemContent{{Text: codexIdentity}})
	if strings.Count(instr, codexIdentity) != 1 {
		t.Errorf("identity should appear once, got: %q", instr)
	}
}
