package modelsources

import (
	"fmt"
	"net/http"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/llm/oauth"
	"shelley.exe.dev/models"
)

// Subscription returns a Source that serves models authenticated with a
// provider subscription via OAuth. Pass the token sources for the providers
// the user has logged into; nil sources are skipped.
//
//   - anthropic: Claude Pro/Max OAuth (Anthropic Messages models).
//   - openai: ChatGPT Plus/Pro OAuth via the Codex backend (Responses models).
//   - kimi: Kimi Code subscription OAuth (Anthropic Messages models).
//
// NOTE: Using subscription credentials from a non-official client is
// undocumented and may violate the provider's terms of service. This source is
// only wired in when the user has explicitly logged in.
func Subscription(anthropicTS, openAITS, kimiTS *oauth.TokenSource) Source {
	providers := map[models.Provider]*providerConn{}
	overrides := map[string]models.Model{}
	if anthropicTS != nil {
		providers[models.ProviderAnthropic] = &providerConn{}
	}
	if openAITS != nil {
		providers[models.ProviderOpenAI] = &providerConn{}
	}
	if kimiTS != nil {
		providers[models.ProviderKimiCoding] = &providerConn{}
		k3 := models.KimiK3Coding()
		overrides[k3.ID] = k3
	}
	return Source{
		label:            "Claude/ChatGPT/Kimi subscription",
		providers:        providers,
		catalogOverrides: overrides,
		buildService: func(m models.Model, _ *providerConn, httpc *http.Client) llm.Service {
			return subscriptionService(m, anthropicTS, openAITS, kimiTS, httpc)
		},
	}
}

// subscriptionService builds the catalog model's normal service (preserving
// every per-model field) and swaps in the OAuth authorizer for the provider.
func subscriptionService(m models.Model, anthropicTS, openAITS, kimiTS *oauth.TokenSource, httpc *http.Client) llm.Service {
	svc := m.Build("", "", httpc)
	switch s := svc.(type) {
	case *ant.Service:
		if m.Provider == models.ProviderKimiCoding {
			s.Auth = ant.KimiOAuthAuth{Tokens: kimiTS}
		} else {
			s.Auth = ant.OAuthAuth{Tokens: anthropicTS}
		}
		return s
	case *oai.ResponsesService:
		s.Auth = oai.OAuthAuth{Tokens: openAITS}
		s.ModelURL = oauth.OpenAICodexBaseURL
		return s
	default:
		panic(fmt.Sprintf("subscription: unsupported service type %T for model %s", svc, m.ID))
	}
}
