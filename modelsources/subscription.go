package modelsources

import (
	"net/http"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/oauth"
	"shelley.exe.dev/models"
)

// Subscription returns a Source that serves Anthropic models authenticated
// with a Claude subscription via OAuth, using the given token source. Only
// Anthropic models are served; subscription OAuth is provider-specific.
//
// NOTE: Using subscription credentials from a non-Claude-Code client is
// undocumented and may violate Anthropic's terms of service. This source is
// only wired in when the user has explicitly logged in.
func Subscription(ts *oauth.TokenSource) Source {
	auth := ant.OAuthAuth{Tokens: ts}
	return Source{
		label: "Claude subscription",
		providers: map[models.Provider]*providerConn{
			// baseURL empty: OAuth talks to the default Anthropic endpoint.
			models.ProviderAnthropic: {},
		},
		buildService: func(m models.Model, _ *providerConn, httpc *http.Client) llm.Service {
			return &ant.Service{
				Auth:            auth,
				Model:           m.APIModelName,
				HTTPC:           httpc,
				ThinkingLevel:   llm.ThinkingLevelMedium,
				SupportsImages_: true,
			}
		},
	}
}
