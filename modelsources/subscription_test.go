package modelsources

import (
	"net/http"
	"testing"

	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/oauth"
	"shelley.exe.dev/models"
)

func TestSubscriptionBuildsOnlyAnthropic(t *testing.T) {
	ts := &oauth.TokenSource{Provider: "anthropic"}
	bs := Build(models.All(), []Source{Subscription(ts)}, &http.Client{}, nil)
	if len(bs) == 0 {
		t.Fatal("subscription source built no models")
	}
	for _, b := range bs {
		if b.Provider != models.ProviderAnthropic {
			t.Errorf("subscription built non-anthropic model %s (%s)", b.ID, b.Provider)
		}
	}
}

func TestSubscriptionUsesOAuthAuthorizer(t *testing.T) {
	ts := &oauth.TokenSource{Provider: "anthropic"}
	bs := Build(models.All(), []Source{Subscription(ts)}, &http.Client{}, nil)
	b := findBuilt(bs, "claude-opus-4.8")
	if b == nil {
		t.Fatal("claude-opus-4.8 not built under subscription")
	}
	svc, ok := b.Service.(*ant.Service)
	if !ok {
		t.Fatalf("service type = %T, want *ant.Service", b.Service)
	}
	if _, ok := svc.Auth.(ant.OAuthAuth); !ok {
		t.Errorf("Auth type = %T, want ant.OAuthAuth", svc.Auth)
	}
}

func TestSubscriptionLabel(t *testing.T) {
	ts := &oauth.TokenSource{Provider: "anthropic"}
	bs := Build(models.All(), []Source{Subscription(ts)}, &http.Client{}, nil)
	b := findBuilt(bs, "claude-opus-4.8")
	if b == nil || b.Source != "Claude subscription" {
		t.Errorf("source label = %q, want \"Claude subscription\"", b.Source)
	}
}
