package modelsources

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/llm/oauth"
	"shelley.exe.dev/models"
)

func TestSubscriptionAnthropicOnly(t *testing.T) {
	ts := &oauth.TokenSource{Provider: "anthropic"}
	bs := Build(models.All(), []Source{Subscription(ts, nil, nil)}, &http.Client{}, nil)
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
	bs := Build(models.All(), []Source{Subscription(nil, ts, nil)}, &http.Client{}, nil)
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
	bs := Build(models.All(), []Source{Subscription(ts, nil, nil)}, &http.Client{}, nil)
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
	bs := Build(models.All(), []Source{Subscription(nil, ts, nil)}, &http.Client{}, nil)
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
	bs := Build(models.All(), []Source{Subscription(ats, ots, nil)}, &http.Client{}, nil)
	if findBuilt(bs, "claude-opus-4.8") == nil {
		t.Error("anthropic model missing")
	}
	if findBuilt(bs, "gpt-5.3-codex") == nil {
		t.Error("openai model missing")
	}
}

func TestSubscriptionKimiOnly(t *testing.T) {
	ts := &oauth.TokenSource{Provider: "kimi"}
	bs := Build(models.All(), []Source{Subscription(nil, nil, ts)}, &http.Client{}, nil)
	if len(bs) != 2 {
		t.Fatalf("Kimi subscription models = %+v", bs)
	}
	b := findBuilt(bs, "kimi-for-coding")
	if b == nil {
		t.Fatal("missing canonical Kimi model")
	}
	if b.APIType != models.APITypeAnthropicMessages || b.APIModelName != "kimi-for-coding" || b.BaseURL != models.DefaultKimiCodingBaseURL {
		t.Fatalf("Kimi metadata = %+v", b)
	}
	svc, ok := b.Service.(*ant.Service)
	if !ok {
		t.Fatalf("service = %T, want *ant.Service", b.Service)
	}
	if auth, ok := svc.Auth.(ant.KimiOAuthAuth); !ok || auth.Tokens != ts {
		t.Fatalf("Auth = %#v, want Kimi token source", svc.Auth)
	}
	if svc.Provider() != string(models.ProviderKimiCoding) || svc.URL != "https://api.kimi.ai/coding/v1/messages" {
		t.Fatalf("Kimi transport provider=%q URL=%q", svc.Provider(), svc.URL)
	}
	if svc.SupportsServerSideWebSearch() || svc.EnableThinkingBinding || !svc.SupportsImages() {
		t.Fatal("Kimi should support images but not Claude web search or thinking binding")
	}
	if svc.MaxTokens != 32768 || svc.DefaultReasoningLevel() != "high" {
		t.Fatalf("Kimi output/reasoning = %d/%s", svc.MaxTokens, svc.DefaultReasoningLevel())
	}
	if want := []llm.ThinkingLevel{llm.ThinkingLevelOff, llm.ThinkingLevelLow, llm.ThinkingLevelHigh, llm.ThinkingLevelMax}; !reflect.DeepEqual(svc.SupportedReasoningLevels(), want) {
		t.Fatalf("reasoning levels = %v, want %v", svc.SupportedReasoningLevels(), want)
	}
}

func TestSubscriptionAllProvidersAndNone(t *testing.T) {
	if bs := Build(models.All(), []Source{Subscription(nil, nil, nil)}, &http.Client{}, nil); len(bs) != 0 {
		t.Fatalf("nil credentials built %d models", len(bs))
	}
	bs := Build(models.All(), []Source{Subscription(&oauth.TokenSource{}, &oauth.TokenSource{}, &oauth.TokenSource{})}, &http.Client{}, nil)
	for _, id := range []string{"claude-opus-4.8", "gpt-5.3-codex", "kimi-for-coding", "kimi-k3-fireworks"} {
		if findBuilt(bs, id) == nil {
			t.Errorf("missing %s", id)
		}
	}
	for _, b := range bs {
		if b.Provider == models.ProviderFireworks {
			t.Errorf("subscription incorrectly enabled Fireworks model %s", b.ID)
		}
	}
}

// Exercise the built subscription service over the actual endpoint URL, with a
// fake transport: streaming, thinking/tool replay, and token changes between
// requests must work without Claude-only headers or payload fields.
func TestSubscriptionKimiStreamingToolRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		id, model string
		maxTokens int
	}{
		{"kimi-for-coding", "kimi-for-coding", 32768},
		{"kimi-k3-fireworks", "k3", 131072},
	} {
		t.Run(tc.id, func(t *testing.T) {
			store := &oauth.Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
			saveToken := func(token string) {
				t.Helper()
				if err := store.Save("kimi", oauth.Token{AccessToken: token, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
			}
			saveToken("first-token")
			ts := &oauth.TokenSource{Provider: "kimi", Store: store}
			calls := 0
			httpc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.String() != "https://api.kimi.ai/coding/v1/messages" {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				wantToken := "first-token"
				if calls == 2 {
					wantToken = "new-token"
				}
				if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
					t.Errorf("Authorization = %q", got)
				}
				for _, header := range []string{"X-API-Key", "X-App", "Anthropic-Beta", "User-Agent"} {
					if got := r.Header.Get(header); got != "" {
						t.Errorf("unexpected %s: %s", header, got)
					}
				}
				if r.Header.Get("Anthropic-Version") != "2023-06-01" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("protocol headers = %v", r.Header)
				}
				var body struct {
					Model        string           `json:"model"`
					Stream       bool             `json:"stream"`
					MaxTokens    int              `json:"max_tokens"`
					System       []map[string]any `json:"system"`
					Thinking     map[string]any   `json:"thinking"`
					OutputConfig map[string]any   `json:"output_config"`
					Tools        []map[string]any `json:"tools"`
					Messages     []struct {
						Content []map[string]any `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Model != tc.model || !body.Stream || body.MaxTokens != tc.maxTokens {
					t.Errorf("request model/stream/tokens = %s/%v/%d", body.Model, body.Stream, body.MaxTokens)
				}
				if len(body.System) != 1 || body.System[0]["text"] != "You are Shelley." {
					t.Errorf("system = %v", body.System)
				}
				if body.Thinking["type"] != "adaptive" || body.Thinking["budget_tokens"] != nil || body.Thinking["block_binding"] != nil || body.OutputConfig["effort"] != "high" {
					t.Errorf("thinking=%v output_config=%v", body.Thinking, body.OutputConfig)
				}
				if len(body.Tools) != 1 || body.Tools[0]["name"] != "read_file" {
					t.Errorf("tools = %v", body.Tools)
				}
				if calls == 2 {
					if len(body.Messages) != 3 {
						t.Fatalf("tool round-trip messages = %+v", body.Messages)
					}
					content := body.Messages[1].Content
					if len(content) != 3 || content[0]["type"] != "thinking" || content[0]["thinking"] != "Inspect the file." || content[2]["type"] != "tool_use" || content[2]["id"] != "tool_1" {
						t.Errorf("assistant history = %+v", content)
					}
					result := body.Messages[2].Content
					if len(result) != 1 || result[0]["type"] != "tool_result" || result[0]["tool_use_id"] != "tool_1" {
						t.Errorf("tool result history = %+v", result)
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(strings.ReplaceAll(kimiToolStream, "kimi-for-coding", tc.model)))}, nil
			})}
			bs := Build(models.All(), []Source{Subscription(nil, nil, ts)}, httpc, nil)
			built := findBuilt(bs, tc.id)
			if built == nil {
				t.Fatalf("missing %s", tc.id)
			}
			svc := built.Service
			var deltas []llm.StreamDelta
			req := &llm.Request{
				System:   []llm.SystemContent{{Text: "You are Shelley."}},
				Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Read README.md"}}}},
				Tools:    []*llm.Tool{{Name: "read_file", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}},
				OnStream: func(d llm.StreamDelta) { deltas = append(deltas, d) },
			}
			resp, err := svc.Do(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Content) != 3 || resp.Content[0].Thinking != "Inspect the file." || resp.Content[0].Signature != "" || resp.Content[1].Text != "Reading." || resp.Content[2].ToolName != "read_file" || string(resp.Content[2].ToolInput) != `{"path":"README.md"}` {
				t.Fatalf("response = %+v", resp)
			}
			if resp.StopReason != llm.StopReasonToolUse || resp.Origin == nil || resp.Origin.Provider != "kimi-coding" {
				t.Fatalf("response stop/origin = %v/%+v", resp.StopReason, resp.Origin)
			}
			if len(deltas) != 2 || deltas[0].Type != "thinking" || deltas[1].Type != "text" {
				t.Errorf("stream deltas = %+v", deltas)
			}
			saveToken("new-token")
			req.Messages = append(req.Messages,
				llm.Message{Role: llm.MessageRoleAssistant, Content: resp.Content, Origin: resp.Origin},
				llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeToolResult, ToolUseID: "tool_1", ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "file contents"}}}}},
			)
			if _, err := svc.Do(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("HTTP calls = %d, want 2", calls)
			}
		})
	}
}

const kimiToolStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"kimi-for-coding","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Inspect the file."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Reading."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tool_1","name":"read_file","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"README.md\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}

event: message_stop
data: {"type":"message_stop"}

`
