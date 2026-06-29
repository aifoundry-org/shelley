package modelsources

import (
	"net/http"
	"testing"

	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/llm/oauth"
	"shelley.exe.dev/models"
)

func TestSubscriptionAnthropicOnly(t *testing.T) {
	ts := &oauth.TokenSource{Provider: "anthropic"}
	bs := Build(models.All(), []Source{Subscription(ts, nil)}, &http.Client{}, nil)
	if len(bs) == 0 {
		t.Fatal("subscription source built no models")
	}
	for _, b := range bs {
		if b.Provider != models.ProviderAnthropic {
			t.Errorf("built non-anthropic model %s (%s)", b.ID, b.Provider)
		}
	}
}

func TestSubscriptionOpenAIOnly(t *testing.T) {
	ts := &oauth.TokenSource{Provider: "openai"}
	bs := Build(models.All(), []Source{Subscription(nil, ts)}, &http.Client{}, nil)
	if len(bs) == 0 {
		t.Fatal("subscription source built no openai models")
	}
	for _, b := range bs {
		if b.Provider != models.ProviderOpenAI {
			t.Errorf("built non-openai model %s (%s)", b.ID, b.Provider)
		}
	}
}

func TestSubscriptionAnthropicUsesOAuthAuthorizer(t *testing.T) {
	ts := &oauth.TokenSource{Provider: "anthropic"}
	bs := Build(models.All(), []Source{Subscription(ts, nil)}, &http.Client{}, nil)
	b := findBuilt(bs, "claude-opus-4.8")
	if b == nil {
		t.Fatal("claude-opus-4.8 not built")
	}
	svc, ok := b.Service.(*ant.Service)
	if !ok {
		t.Fatalf("service type = %T, want *ant.Service", b.Service)
	}
	if _, ok := svc.Auth.(ant.OAuthAuth); !ok {
		t.Errorf("Auth type = %T, want ant.OAuthAuth", svc.Auth)
	}
}

func TestSubscriptionOpenAIUsesOAuthAndCodexBackend(t *testing.T) {
	ts := &oauth.TokenSource{Provider: "openai"}
	bs := Build(models.All(), []Source{Subscription(nil, ts)}, &http.Client{}, nil)
	b := findBuilt(bs, "gpt-5.3-codex")
	if b == nil {
		t.Fatal("gpt-5.3-codex not built")
	}
	svc, ok := b.Service.(*oai.ResponsesService)
	if !ok {
		t.Fatalf("service type = %T, want *oai.ResponsesService", b.Service)
	}
	if _, ok := svc.Auth.(oai.OAuthAuth); !ok {
		t.Errorf("Auth type = %T, want oai.OAuthAuth", svc.Auth)
	}
	if svc.ModelURL != oauth.OpenAICodexBaseURL {
		t.Errorf("ModelURL = %q, want Codex backend %q", svc.ModelURL, oauth.OpenAICodexBaseURL)
	}
}

func TestSubscriptionBothProviders(t *testing.T) {
	ats := &oauth.TokenSource{Provider: "anthropic"}
	ots := &oauth.TokenSource{Provider: "openai"}
	bs := Build(models.All(), []Source{Subscription(ats, ots)}, &http.Client{}, nil)
	if findBuilt(bs, "claude-opus-4.8") == nil {
		t.Error("anthropic model missing")
	}
	if findBuilt(bs, "gpt-5.3-codex") == nil {
		t.Error("openai model missing")
	}
}
