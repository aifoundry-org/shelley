package models

import (
	"cmp"
	"net/http"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/gem"
	"shelley.exe.dev/llm/oai"
)

// ServiceConfig is everything that legitimately differs between the places
// that construct an LLM client: built-in catalog models, LLM integrations,
// custom models, and provider connections. NewService is the only place that
// turns one into an llm.Service, so per-protocol defaults cannot drift.
type ServiceConfig struct {
	APIType APIType
	// URL is the full protocol endpoint (see APIEndpoint); "" uses the
	// provider package default.
	URL    string
	APIKey string
	HTTPC  *http.Client

	Model          string // wire model name
	SupportsImages bool
	// OpenAIModel carries catalog metadata for OpenAI-protocol models
	// (verbosity, apply_patch, canonical URL). Model and SupportsImages
	// replace its ModelName and SupportsImages.
	OpenAIModel oai.Model
	// ProviderName is the upstream vendor ("openai", "xai", "fireworks",
	// ...). OpenAI-protocol services key wire quirks and server tools on it;
	// Anthropic-protocol services report it as their provider identity
	// ("" means anthropic).
	ProviderName string
	// ThinkingBinding sends Anthropic drop_block binding controls. Only set
	// it when the backend is known to be first-party Anthropic; the service
	// already enables it for api.anthropic.com.
	ThinkingBinding bool

	MaxTokens        int // 0 uses the models.dev limit or the service default
	ReasoningEffort  string
	ReasoningReplay  oai.ReasoningReplay
	DisableReasoning bool
	// ThinkingLevel is the service's default level; zero uses medium.
	ThinkingLevel llm.ThinkingLevel

	// ReasoningLevels, ForceAdaptiveThinking and AllowEmptyThinkingSignature
	// adapt the Anthropic protocol to compatible non-Claude providers (see
	// ant.Service); other protocols ignore them.
	ReasoningLevels             []llm.ThinkingLevel
	ForceAdaptiveThinking       bool
	AllowEmptyThinkingSignature bool
}

// APIEndpoint appends apiType's protocol path to a bare base URL (origin plus
// any non-API prefix). An empty base stays empty so the service default applies.
func APIEndpoint(apiType APIType, baseURL string) string {
	if baseURL == "" {
		return ""
	}
	switch apiType {
	case APITypeAnthropicMessages:
		return baseURL + "/v1/messages"
	case APITypeOpenAIResponses, APITypeOpenAIChat:
		return baseURL + "/v1"
	case APITypeGemini:
		return baseURL + "/v1beta"
	}
	return baseURL
}

// NewService builds the llm.Service for cfg, or returns nil for an API type
// with no network client.
func NewService(cfg ServiceConfig) llm.Service {
	oaiModel := cfg.OpenAIModel
	oaiModel.ModelName, oaiModel.SupportsImages = cfg.Model, cfg.SupportsImages
	thinkingLevel := cmp.Or(cfg.ThinkingLevel, llm.ThinkingLevelMedium)
	switch cfg.APIType {
	case APITypeAnthropicMessages:
		return &ant.Service{
			URL: cfg.URL, Auth: ant.APIKeyAuth{Key: cfg.APIKey}, HTTPC: cfg.HTTPC,
			Model: cfg.Model, SupportsImages_: cfg.SupportsImages, MaxTokens: cfg.MaxTokens,
			ProviderName:  cfg.ProviderName,
			ThinkingLevel: thinkingLevel, EnableThinkingBinding: cfg.ThinkingBinding,
			DisableReasoning: cfg.DisableReasoning, ReasoningLevels: cfg.ReasoningLevels,
			ForceAdaptiveThinking:       cfg.ForceAdaptiveThinking,
			AllowEmptyThinkingSignature: cfg.AllowEmptyThinkingSignature,
		}
	case APITypeOpenAIResponses:
		return &oai.ResponsesService{
			ModelURL: cfg.URL, Auth: oai.APIKeyAuth{Key: cfg.APIKey}, HTTPC: cfg.HTTPC,
			Model: oaiModel, MaxTokens: cfg.MaxTokens, ProviderName: cfg.ProviderName,
			ThinkingLevel: thinkingLevel, ReasoningEffort: cfg.ReasoningEffort,
			ReasoningReplay: cfg.ReasoningReplay, DisableReasoning: cfg.DisableReasoning,
		}
	case APITypeOpenAIChat:
		return &oai.Service{
			ModelURL: cfg.URL, APIKey: cfg.APIKey, HTTPC: cfg.HTTPC,
			Model: oaiModel, MaxTokens: cfg.MaxTokens, ProviderName: cfg.ProviderName,
			ReasoningEffort: cfg.ReasoningEffort,
			ReasoningReplay: cfg.ReasoningReplay, DisableReasoning: cfg.DisableReasoning,
		}
	case APITypeGemini:
		return &gem.Service{
			URL: cfg.URL, APIKey: cfg.APIKey, HTTPC: cfg.HTTPC,
			Model: cfg.Model, SupportsImages_: cfg.SupportsImages, MaxTokens: cfg.MaxTokens,
			ReasoningEffort: cfg.ReasoningEffort,
		}
	}
	return nil
}

// customAPITypes maps a stored custom-model provider_type to its protocol.
var customAPITypes = map[string]APIType{
	"anthropic":        APITypeAnthropicMessages,
	"openai":           APITypeOpenAIChat,
	"openai-responses": APITypeOpenAIResponses,
	"gemini":           APITypeGemini,
}

// customServiceConfig is the ServiceConfig for a custom model row. Custom
// endpoints are arbitrary, so ProviderName is the generic "openai" (left
// empty for the Anthropic protocol, which then reports anthropic) and
// Anthropic thinking binding is left to the service's native-URL detection.
func customServiceConfig(model *generated.Model, httpc *http.Client) (ServiceConfig, bool) {
	apiType, ok := customAPITypes[model.ProviderType]
	providerName := "openai"
	if apiType == APITypeAnthropicMessages {
		providerName = ""
	}
	return ServiceConfig{
		APIType: apiType, URL: model.Endpoint, APIKey: model.ApiKey, HTTPC: httpc,
		Model:           model.ModelName,
		SupportsImages:  ResolveSupportsImages(model.Endpoint, model.ModelName, model.ImageSupport),
		ProviderName:    providerName,
		MaxTokens:       int(model.MaxTokens),
		ReasoningEffort: model.ReasoningEffort,
		ReasoningReplay: oai.ReasoningReplay(model.ReasoningReplay),
	}, ok
}
