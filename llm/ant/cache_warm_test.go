package ant

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

type recordingTransport struct {
	bodies   []map[string]any
	response func() *http.Response
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	r.bodies = append(r.bodies, body)
	return r.response(), nil
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

// A warm must hit the same cache entry as the turn it replays: every cache-key
// field must match what Do sends, and only output generation may change.
func TestWarmCacheReplaysProviderPrefixWithoutOutput(t *testing.T) {
	transport := &recordingTransport{}
	s := &Service{Auth: APIKeyAuth{Key: "k"}, Model: "claude-opus-4-7", HTTPC: &http.Client{Transport: transport}}
	req := &llm.Request{
		System:        []llm.SystemContent{{Text: "personal assistant", Cache: true}},
		Tools:         []*llm.Tool{{Name: "lookup", InputSchema: llm.MustSchema(`{"type":"object","properties":{}}`), Cache: true}},
		ThinkingLevel: llm.ThinkingLevelMedium,
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "remember the garden plan", Cache: true},
		}}},
	}
	transport.response = func() *http.Response { return jsonResponse(400, `{"type":"error"}`) }
	if _, err := s.Do(t.Context(), req); err == nil {
		t.Fatal("expected captured Do request to fail")
	}
	transport.response = func() *http.Response {
		return jsonResponse(200, `{"model":"claude-opus-4-7","content":[],"stop_reason":"max_tokens",
			"usage":{"input_tokens":3,"cache_read_input_tokens":2048,"output_tokens":0}}`)
	}
	usage, err := s.WarmCache(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if usage.CacheReadInputTokens != 2048 || usage.OutputTokens != 0 || usage.Model != "claude-opus-4-7" {
		t.Fatalf("usage = %+v", usage)
	}
	turn, warm := transport.bodies[0], transport.bodies[1]
	if warm["max_tokens"] != float64(0) || warm["stream"] != nil {
		t.Fatalf("warm generated output: max_tokens=%v stream=%v", warm["max_tokens"], warm["stream"])
	}
	delete(turn, "max_tokens")
	delete(turn, "stream")
	delete(warm, "max_tokens")
	a, _ := json.Marshal(turn)
	b, _ := json.Marshal(warm)
	if string(a) != string(b) {
		t.Fatalf("warm changed the cached prefix:\nturn %s\nwarm %s", a, b)
	}
}

func TestWarmCacheRejectsOutputAndBudgetThinking(t *testing.T) {
	transport := &recordingTransport{response: func() *http.Response {
		return jsonResponse(200, `{"content":[{"type":"tool_use","id":"x","name":"lookup","input":{}}],"stop_reason":"tool_use","usage":{}}`)
	}}
	req := &llm.Request{Messages: []llm.Message{llm.UserStringMessage("hi")}}
	s := &Service{Auth: APIKeyAuth{Key: "k"}, Model: "claude-opus-4-7", HTTPC: &http.Client{Transport: transport}}
	if _, err := s.WarmCache(t.Context(), req); err == nil || !strings.Contains(err.Error(), "generated output") {
		t.Fatalf("returned tool call was accepted: %v", err)
	}
	req.ThinkingLevel = llm.ThinkingLevelMedium
	s.Model = "claude-sonnet-4-5"
	if _, err := s.WarmCache(t.Context(), req); !errors.Is(err, llm.ErrCacheWarmUnsupported) {
		t.Fatalf("budget thinking warm err = %v", err)
	}
	if len(transport.bodies) != 1 {
		t.Fatalf("unsupported warm reached the provider")
	}
}
