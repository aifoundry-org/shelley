package loop

import (
	"context"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func imageToolResult(text, data string) llm.Message {
	results := []llm.Content{{Type: llm.ContentTypeText, MediaType: "image/png", Data: data}}
	if text != "" {
		results = append([]llm.Content{{Type: llm.ContentTypeText, Text: text}}, results...)
	}
	return llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type:       llm.ContentTypeToolResult,
			ToolUseID:  "screenshot",
			ToolResult: results,
		}},
	}
}

func TestElideConsumedToolResultImages(t *testing.T) {
	consumed := imageToolResult("saved screenshot", "old-image")
	trailing := imageToolResult("new screenshot", "new-image")
	messages := []llm.Message{
		consumed,
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{llm.StringContent("I inspected it")}},
		trailing,
	}

	got := elideConsumedToolResultImages(messages)
	if len(got[0].Content[0].ToolResult) != 1 || got[0].Content[0].ToolResult[0].Text != "saved screenshot" {
		t.Fatalf("consumed result = %+v, want text only", got[0].Content[0].ToolResult)
	}
	if got[2].Content[0].ToolResult[1].Data != "new-image" {
		t.Fatal("trailing unseen image was elided")
	}
	if messages[0].Content[0].ToolResult[1].Data != "old-image" {
		t.Fatal("durable history was mutated")
	}
}

func TestElideConsumedImageOnlyToolResultLeavesPlaceholder(t *testing.T) {
	messages := []llm.Message{
		imageToolResult("", "old-image"),
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{llm.StringContent("seen")}},
	}

	got := elideConsumedToolResultImages(messages)
	results := got[0].Content[0].ToolResult
	if len(results) != 1 || results[0].Text != omittedToolResultImage || results[0].Data != "" {
		t.Fatalf("result = %+v, want omission placeholder", results)
	}
}

func TestElideConsumedToolResultImagesKeepsUserImages(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, MediaType: "image/png", Data: "user-image"}}},
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{llm.StringContent("seen")}},
	}

	got := elideConsumedToolResultImages(messages)
	if got[0].Content[0].Data != "user-image" {
		t.Fatal("ordinary user image was elided")
	}
}

func TestProcessLLMRequestElidesConsumedToolResultImages(t *testing.T) {
	var mu sync.Mutex
	var requests []*llm.Request
	service := NewRequestCapturingService(&mu, &requests)
	turn := NewLoop(Config{
		LLM: service,
		History: []llm.Message{
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{{
				ID: "screenshot", Type: llm.ContentTypeToolUse, ToolName: "browser",
			}}},
			imageToolResult("saved screenshot", "old-image"),
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{llm.StringContent("inspected")}},
		},
		RecordMessage: func(context.Context, llm.Message, llm.Usage, []llm.PurposedUsage) error { return nil },
	})
	turn.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{llm.StringContent("hello")},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := turn.ProcessOneTurn(ctx); err != nil {
		t.Fatalf("ProcessOneTurn: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	results := requests[0].Messages[1].Content[0].ToolResult
	if len(results) != 1 || results[0].Text != "saved screenshot" || results[0].Data != "" {
		t.Fatalf("sent result = %+v, want text only", results)
	}
}
