package ant

import (
	"encoding/json"
	"reflect"
	"testing"

	"shelley.exe.dev/llm"
)

func TestCompatibleAdaptiveThinking(t *testing.T) {
	s := &Service{
		Model: "kimi-for-coding", URL: "https://api.kimi.ai/coding/v1/messages",
		ForceAdaptiveThinking: true, ThinkingLevel: llm.ThinkingLevelHigh,
		ReasoningLevels: []llm.ThinkingLevel{llm.ThinkingLevelOff, llm.ThinkingLevelLow, llm.ThinkingLevelHigh, llm.ThinkingLevelMax},
	}
	for _, tc := range []struct {
		level  llm.ThinkingLevel
		effort string
	}{
		{llm.ThinkingLevelDefault, "high"},
		{llm.ThinkingLevelOff, ""},
		{llm.ThinkingLevelMinimal, "low"},
		{llm.ThinkingLevelLow, "low"},
		{llm.ThinkingLevelMedium, "low"},
		{llm.ThinkingLevelHigh, "high"},
		{llm.ThinkingLevelXHigh, "high"},
		{llm.ThinkingLevelMax, "max"},
	} {
		t.Run(tc.level.String(), func(t *testing.T) {
			req := s.fromLLMRequest(&llm.Request{ThinkingLevel: tc.level})
			want := &thinking{Type: "adaptive", Display: "summarized"}
			var output *outputConfig
			if tc.effort == "" {
				want = &thinking{Type: "disabled"}
			} else {
				output = &outputConfig{Effort: tc.effort}
			}
			if !reflect.DeepEqual(req.Thinking, want) || !reflect.DeepEqual(req.OutputConfig, output) {
				t.Fatalf("thinking/output = %+v/%+v, want %+v/%+v", req.Thinking, req.OutputConfig, want, output)
			}
		})
	}
}

func TestUnsignedThinkingIsProviderOptIn(t *testing.T) {
	msg := llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{
		{Type: llm.ContentTypeThinking, Thinking: "Reasoning"},
		{Type: llm.ContentTypeThinking},
		{Type: llm.ContentTypeText, Text: "Answer"},
	}}
	for _, allow := range []bool{false, true} {
		s := &Service{AllowEmptyThinkingSignature: allow}
		wire := s.fromLLMMessage(msg)
		want := 1
		if allow {
			want = 2
		}
		if len(wire.Content) != want {
			t.Fatalf("allow unsigned=%v: content = %+v", allow, wire.Content)
		}
		if allow {
			data, err := json.Marshal(wire.Content[0])
			if err != nil {
				t.Fatal(err)
			}
			var block map[string]any
			if err := json.Unmarshal(data, &block); err != nil {
				t.Fatal(err)
			}
			if block["type"] != "thinking" || block["thinking"] != "Reasoning" || block["signature"] != "" {
				t.Fatalf("unsigned wire block = %s", data)
			}
		}
	}
}

func TestKimiK3DoesNotDisableThinking(t *testing.T) {
	s := &Service{
		Model: "k3", URL: "https://api.kimi.ai/coding/v1/messages",
		ForceAdaptiveThinking: true, ThinkingLevel: llm.ThinkingLevelHigh,
		ReasoningLevels: []llm.ThinkingLevel{llm.ThinkingLevelLow, llm.ThinkingLevelHigh, llm.ThinkingLevelMax},
	}
	for _, level := range []llm.ThinkingLevel{llm.ThinkingLevelDefault, llm.ThinkingLevelOff} {
		req := s.fromLLMRequest(&llm.Request{ThinkingLevel: level})
		if req.Thinking == nil || req.Thinking.Type != "adaptive" || req.OutputConfig == nil {
			t.Fatalf("K3 level %v disabled thinking: %+v", level, req)
		}
	}
}
