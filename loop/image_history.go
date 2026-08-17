package loop

import (
	"strings"

	"shelley.exe.dev/llm"
)

const omittedToolResultImage = "[Image omitted from prior tool result]"

// elideConsumedToolResultImages removes image payloads from tool results that
// have already been followed by an assistant message. The returned request
// copy is changed; durable history remains untouched. A trailing tool result is
// kept because the model has not seen it yet.
func elideConsumedToolResultImages(messages []llm.Message) []llm.Message {
	var out []llm.Message
	hasLaterAssistant := false

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == llm.MessageRoleAssistant {
			hasLaterAssistant = true
			continue
		}
		if !hasLaterAssistant {
			continue
		}

		var content []llm.Content
		for j, block := range msg.Content {
			if block.Type != llm.ContentTypeToolResult {
				continue
			}

			results := make([]llm.Content, 0, len(block.ToolResult))
			changed := false
			for _, result := range block.ToolResult {
				if strings.HasPrefix(strings.ToLower(result.MediaType), "image/") && result.Data != "" {
					changed = true
					if result.Text != "" {
						result.MediaType = ""
						result.Data = ""
						results = append(results, result)
					}
					continue
				}
				results = append(results, result)
			}
			if !changed {
				continue
			}
			if len(results) == 0 {
				results = append(results, llm.StringContent(omittedToolResultImage))
			}
			if content == nil {
				content = append([]llm.Content(nil), msg.Content...)
			}
			block.ToolResult = results
			content[j] = block
		}
		if content == nil {
			continue
		}
		if out == nil {
			out = append([]llm.Message(nil), messages...)
		}
		msg.Content = content
		out[i] = msg
	}

	if out == nil {
		return messages
	}
	return out
}
