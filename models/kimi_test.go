package models

import (
	"net/http"
	"reflect"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/models/modelsdev"
)

func TestKimiCodingCatalog(t *testing.T) {
	m := ByID("kimi-for-coding")
	if m == nil {
		t.Fatal("Kimi Coding model missing")
	}
	if m.Description != "Kimi For Coding" || m.Provider != ProviderKimiCoding || m.APIType != APITypeAnthropicMessages || m.APIModelName != "kimi-for-coding" || m.DefaultBaseURL != DefaultKimiCodingBaseURL {
		t.Fatalf("Kimi Coding catalog = %+v", m)
	}
	client := &http.Client{}
	for _, base := range []string{"", "https://custom.example/coding"} {
		svc, ok := m.Build(base, "api-key", client).(*ant.Service)
		if !ok {
			t.Fatal("Kimi Coding must use Anthropic Messages")
		}
		wantBase := base
		if wantBase == "" {
			wantBase = DefaultKimiCodingBaseURL
		}
		if svc.URL != wantBase+"/v1/messages" || svc.Model != m.APIModelName || svc.HTTPC != client {
			t.Fatalf("built service = %+v", svc)
		}
		if auth, ok := svc.Auth.(ant.APIKeyAuth); !ok || auth.Key != "api-key" {
			t.Fatalf("catalog auth = %#v", svc.Auth)
		}
		if !svc.ForceAdaptiveThinking || !svc.AllowEmptyThinkingSignature || svc.EnableThinkingBinding {
			t.Fatalf("Kimi compatibility options = %+v", svc)
		}
	}
	for _, id := range []string{"kimi-k2.6-fireworks", "kimi-k2.7-code-fireworks", "kimi-k3-fireworks"} {
		fw := ByID(id)
		if fw == nil || fw.Provider != ProviderFireworks || fw.APIType != APITypeOpenAIChat {
			t.Errorf("Fireworks entry %s must stay independent", id)
		}
	}
}

func TestKimiK3CodingRoute(t *testing.T) {
	m := KimiK3Coding()
	if ByID("kimi-k3-subscription") != nil {
		t.Fatal("K3 must have only one catalog ID")
	}
	if m.ID != "kimi-k3-fireworks" || m.Provider != ProviderKimiCoding || m.APIModelName != "k3" || m.Description != "Kimi K3" || m.APIType != APITypeAnthropicMessages {
		t.Fatalf("Kimi K3 subscription catalog = %+v", m)
	}
	svc := m.Build("", "", &http.Client{}).(*ant.Service)
	wantLevels := []llm.ThinkingLevel{llm.ThinkingLevelLow, llm.ThinkingLevelHigh, llm.ThinkingLevelMax}
	if svc.URL != "https://api.kimi.ai/coding/v1/messages" || svc.Model != "k3" || svc.MaxTokens != 131072 || svc.DefaultReasoningLevel() != "high" || !reflect.DeepEqual(svc.SupportedReasoningLevels(), wantLevels) {
		t.Fatalf("Kimi K3 configuration = %+v", svc)
	}
	if !svc.SupportsImages() || !svc.AllowEmptyThinkingSignature || !svc.ForceAdaptiveThinking || svc.EnableThinkingBinding || svc.SupportsServerSideWebSearch() {
		t.Fatalf("Kimi K3 compatibility = %+v", svc)
	}
	if context, found := modelsdev.LookupContextLimit(svc.URL, "k3"); !found || context != 1048576 {
		t.Fatalf("K3 context = %d (%v), want 1048576", context, found)
	}
	if output, found := modelsdev.LookupAnthropicOutputLimit(svc.URL, "k3"); !found || output != 131072 {
		t.Fatalf("K3 output = %d (%v), want 131072", output, found)
	}
}
