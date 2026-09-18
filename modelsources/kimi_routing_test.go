package modelsources

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/llm/oauth"
	"shelley.exe.dev/models"
)

// Use discovery, not a synthetic Source, to exercise production integration ID matching.
func discoveredKimiIntegration(t *testing.T) Source {
	t.Helper()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.String() {
		case "https://reflection.int.exe.xyz/integrations":
			body = `{"integrations":[{"name":"llm","type":"llm"}]}`
		case "https://llm.int.exe.xyz/models.json":
			body = `{"schema_version":1,"models":[{"id":"fireworks/kimi-k3","provider":"fireworks","native_id":"accounts/fireworks/models/kimi-k3","apis":["openai_chat","anthropic_messages"]}]}`
		default:
			t.Fatalf("unexpected discovery request: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	result := discoverLLMIntegrations(t.Context(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), exeenv.FromHostname("box.exe.xyz"))
	if !result.Found || len(result.Integrations) != 1 {
		t.Fatalf("discovery = %+v", result)
	}
	return LLMIntegration(result.Integrations[0], "")
}

func TestKimiK3SourceRouting(t *testing.T) {
	ts := &oauth.TokenSource{Provider: "kimi"}
	subscription := Subscription(nil, nil, ts)
	for _, tc := range []struct {
		name             string
		paid             Source
		base, key, label string
	}{
		{"gateway", Gateway("https://gateway.example", "", "", ""), "https://gateway.example/fireworks/inference", "implicit", "exe.dev gateway"},
		{"env", Env("", "", "", "paid-key"), models.DefaultFireworksBaseURL, "paid-key", "$FIREWORKS_API_KEY"},
		{"integration", discoveredKimiIntegration(t), "https://llm.int.exe.xyz", "implicit", "llm.int.exe.xyz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := models.All()
			for _, state := range []struct {
				name    string
				sources []Source
				coding  bool
			}{
				{"logged in", []Source{subscription, tc.paid}, true},
				{"logged out", []Source{Subscription(nil, nil, nil), tc.paid}, false},
				{"paid source first", []Source{tc.paid, subscription}, false},
			} {
				t.Run(state.name, func(t *testing.T) {
					built := Build(catalog, state.sources, &http.Client{}, nil)
					count := 0
					var ids []string
					for _, b := range built {
						ids = append(ids, b.ID)
						if strings.Contains(b.ID, "k3") {
							count++
						}
					}
					if count != 1 {
						t.Fatalf("K3 entries = %d: %v", count, ids)
					}
					b := findBuilt(built, "kimi-k3-fireworks")
					if b == nil {
						t.Fatal("established K3 picker ID missing")
					}
					if b.DisplayName != b.ID {
						t.Fatalf("display name = %q", b.DisplayName)
					}
					if state.coding {
						if b.Provider != models.ProviderKimiCoding || b.APIType != models.APITypeAnthropicMessages || b.APIModelName != "k3" || b.BaseURL != models.DefaultKimiCodingBaseURL || b.Source != subscription.label || b.ReleaseDate != modelReleaseDate(b.BaseURL, "k3") {
							t.Fatalf("subscription metadata = %+v", b)
						}
						svc, ok := b.Service.(*ant.Service)
						if !ok || svc.Model != "k3" || svc.URL != "https://api.kimi.ai/coding/v1/messages" || svc.Provider() != "kimi-coding" {
							t.Fatalf("subscription service = %+v", b.Service)
						}
						if auth, ok := svc.Auth.(ant.KimiOAuthAuth); !ok || auth.Tokens != ts {
							t.Fatalf("subscription auth = %#v", svc.Auth)
						}
					} else {
						if b.Provider != models.ProviderFireworks || b.APIType != models.APITypeOpenAIChat || b.APIModelName != "accounts/fireworks/models/kimi-k3" || b.BaseURL != tc.base || b.Source != tc.label || b.ReleaseDate != modelReleaseDate(tc.base, b.APIModelName) {
							t.Fatalf("paid metadata = %+v", b)
						}
						svc, ok := b.Service.(*oai.Service)
						if !ok || svc.Provider() != "fireworks" || svc.APIKey != tc.key || svc.Model.ModelName != b.APIModelName {
							t.Fatalf("paid service = %+v", b.Service)
						}
						base := svc.ModelURL
						if base == "" {
							base = svc.Model.URL
						}
						if base != tc.base+"/v1" {
							t.Fatalf("paid URL = %q", base)
						}
					}
					tiers := models.AssignTiers(ids)
					if tiers[b.ID] != models.Tier1 {
						t.Fatalf("K3 tier = %v", tiers[b.ID])
					}
					for _, id := range []string{"kimi-k2.6-fireworks", "kimi-k2.7-code-fireworks"} {
						if older := findBuilt(built, id); older != nil && (older.Provider != models.ProviderFireworks || tiers[id] != models.Tier2) {
							t.Fatalf("older Kimi route/tier changed: %+v, %v", older, tiers[id])
						}
					}
				})
			}
			if m := models.ByID("kimi-k3-fireworks"); m.Provider != models.ProviderFireworks {
				t.Fatal("subscription mutated global catalog")
			}
		})
	}
}

func TestKimiK3OverrideReservesLogicalID(t *testing.T) {
	src := Subscription(nil, nil, &oauth.TokenSource{Provider: "kimi"})
	if !nonIntegrationModelIDs(models.All(), []Source{src})["kimi-k3-fireworks"] {
		t.Fatal("subscription must reserve the overridden logical ID")
	}
	// An unrelated provider with the same short ID must be qualified, not dropped.
	integ := LLMIntegration(&LLMIntegrationConfig{Host: "other.example", URL: "https://other.example", Models: []IntegrationModel{{ID: "other/kimi-k3-fireworks", Provider: "other", APIs: []string{"openai_chat"}}}}, "")
	built := Build(models.All(), []Source{integ, src}, &http.Client{}, nil)
	if findBuilt(built, "other/kimi-k3-fireworks") == nil || findBuilt(built, "kimi-k3-fireworks") == nil {
		t.Fatalf("colliding models lost: %+v", built)
	}
}

func TestKimiK3ErrorsNeverUsePaidSource(t *testing.T) {
	for _, status := range []int{0, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			store := &oauth.Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
			if status != 0 {
				if err := store.Save("kimi", oauth.Token{AccessToken: "test-token", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://api.kimi.ai/coding/v1/messages" {
					t.Fatalf("paid request after OAuth failure: %s", r.URL)
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"subscription unavailable"}}`))}, nil
			})}
			subscription := Subscription(nil, nil, &oauth.TokenSource{Provider: "kimi", Store: store})
			built := Build(models.All(), []Source{subscription, Gateway("https://gateway.example", "", "", ""), Env("", "", "", "paid-key"), discoveredKimiIntegration(t)}, client, nil)
			b := findBuilt(built, "kimi-k3-fireworks")
			if b == nil {
				t.Fatal("K3 missing")
			}
			_, err := b.Service.Do(ctx, &llm.Request{
				Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}}},
				// Cancel quota retries deterministically without sleeping; retries must stay on Kimi.
				OnRetry: func(event llm.RetryEvent) {
					if event.Provider != "kimi-coding" {
						t.Errorf("retry provider = %q", event.Provider)
					}
					cancel()
				},
			})
			if err == nil {
				t.Fatal("subscription failure was swallowed")
			}
			wantCalls := 1
			if status == 0 {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}
