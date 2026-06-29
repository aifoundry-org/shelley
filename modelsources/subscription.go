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
//
// NOTE: Using subscription credentials from a non-official client is
// undocumented and may violate the provider's terms of service. This source is
// only wired in when the user has explicitly logged in.
func Subscription(anthropicTS, openAITS *oauth.TokenSource) Source {
	providers := map[models.Provider]*providerConn{}
	if anthropicTS != nil {
		providers[models.ProviderAnthropic] = &providerConn{}
	}
	if openAITS != nil {
		providers[models.ProviderOpenAI] = &providerConn{}
	}
	return Source{
		label:     "Claude/ChatGPT subscription",
		providers: providers,
		buildService: func(m models.Model, _ *providerConn, httpc *http.Client) llm.Service {
			return subscriptionService(m, anthropicTS, openAITS, httpc)
		},
	}
}

// subscriptionService builds the catalog model's normal service (preserving
// every per-model field) and swaps in the OAuth authorizer for the provider.
func subscriptionService(m models.Model, anthropicTS, openAITS *oauth.TokenSource, httpc *http.Client) llm.Service {
	svc := m.Build("", "", httpc)
	switch s := svc.(type) {
	case *ant.Service:
		s.Auth = ant.OAuthAuth{Tokens: anthropicTS}
		return s
	case *oai.ResponsesService:
		s.Auth = oai.OAuthAuth{Tokens: openAITS}
		s.ModelURL = oauth.OpenAICodexBaseURL
		return s
	default:
		panic(fmt.Sprintf("subscription: unsupported service type %T for model %s", svc, m.ID))
	}
}
