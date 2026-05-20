package translation

import (
	"encoding/json"
	"testing"
)

func TestTranslateAnthropicRequestToOpenAI_Basic(t *testing.T) {
	body := map[string]interface{}{
		"model":       "claude-3-opus",
		"max_tokens":  4096,
		"temperature": 0.7,
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "Hello"},
		},
	}

	result, err := TranslateAnthropicRequestToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result["model"] != "claude-3-opus" {
		t.Errorf("model mismatch: got %v", result["model"])
	}
	if result["max_tokens"] != 4096 {
		t.Errorf("max_tokens mismatch: got %v", result["max_tokens"])
	}

	msgs, ok := result["messages"].([]interface{})
	if !ok || len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %v", result["messages"])
	}
	msg := msgs[0].(map[string]interface{})
	if msg["role"] != "user" {
		t.Errorf("expected user role, got %v", msg["role"])
	}
}

func TestTranslateAnthropicRequestToOpenAI_System(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-3-opus",
		"system": []interface{}{
			map[string]interface{}{"type": "text", "text": "You are helpful."},
			map[string]interface{}{"type": "text", "text": "Be concise."},
		},
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "Hello"},
		},
	}

	result, err := TranslateAnthropicRequestToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs, ok := result["messages"].([]interface{})
	if !ok || len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}

	sys := msgs[0].(map[string]interface{})
	if sys["role"] != "system" {
		t.Errorf("expected first message role=system, got %v", sys["role"])
	}
	if sys["content"] != "You are helpful.\nBe concise." {
		t.Errorf("expected combined system text, got %q", sys["content"])
	}
}

func TestTranslateAnthropicRequestToOpenAI_AssistantWithToolUse(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-3-opus",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": "I'll check that.",
					},
					map[string]interface{}{
						"type": "tool_use",
						"id":   "tool_123",
						"name": "get_weather",
						"input": map[string]interface{}{
							"location": "NYC",
						},
					},
				},
			},
		},
	}

	result, err := TranslateAnthropicRequestToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs, _ := result["messages"].([]interface{})
	msg := msgs[0].(map[string]interface{})

	if msg["role"] != "assistant" {
		t.Errorf("expected assistant role, got %v", msg["role"])
	}
	if msg["content"] != "I'll check that." {
		t.Errorf("expected content, got %v", msg["content"])
	}

	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool_call, got %v", msg["tool_calls"])
	}
	if toolCalls[0]["id"] != "tool_123" {
		t.Errorf("expected tool id=tool_123, got %v", toolCalls[0]["id"])
	}
	fn := toolCalls[0]["function"].(map[string]interface{})
	if fn["name"] != "get_weather" {
		t.Errorf("expected name=get_weather, got %v", fn["name"])
	}
}

func TestTranslateAnthropicRequestToOpenAI_UserWithToolResult(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-3-opus",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "tool_123",
						"content":     `{"temp":72}`,
					},
				},
			},
		},
	}

	result, err := TranslateAnthropicRequestToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs, _ := result["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg := msgs[0].(map[string]interface{})
	if msg["role"] != "tool" {
		t.Errorf("expected tool role, got %v", msg["role"])
	}
	if msg["tool_call_id"] != "tool_123" {
		t.Errorf("expected tool_call_id=tool_123, got %v", msg["tool_call_id"])
	}
}

func TestTranslateAnthropicRequestToOpenAI_UserWithImage(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-3-opus",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type": "image",
						"source": map[string]interface{}{
							"type":       "base64",
							"media_type": "image/png",
							"data":       "abc123",
						},
					},
				},
			},
		},
	}

	result, err := TranslateAnthropicRequestToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs, _ := result["messages"].([]interface{})
	msg := msgs[0].(map[string]interface{})
	content, _ := msg["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	img := content[0].(map[string]interface{})
	if img["type"] != "image_url" {
		t.Errorf("expected image_url type, got %v", img["type"])
	}
	imgURL := img["image_url"].(map[string]interface{})
	if imgURL["url"] != "data:image/png;base64,abc123" {
		t.Errorf("unexpected image url: %v", imgURL["url"])
	}
}

func TestTranslateAnthropicRequestToOpenAI_Tools(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-3-opus",
		"tools": []interface{}{
			map[string]interface{}{
				"name":         "get_weather",
				"description":  "Get weather info",
				"input_schema": map[string]interface{}{"type": "object"},
			},
		},
		"tool_choice": map[string]interface{}{
			"type": "tool",
			"name": "get_weather",
		},
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "What's the weather?"},
		},
	}

	result, err := TranslateAnthropicRequestToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tools, ok := result["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %v", result["tools"])
	}
	tool := tools[0].(map[string]interface{})
	if tool["type"] != "function" {
		t.Errorf("expected type=function, got %v", tool["type"])
	}
	fn := tool["function"].(map[string]interface{})
	if fn["name"] != "get_weather" {
		t.Errorf("expected name=get_weather, got %v", fn["name"])
	}

	tc, ok := result["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool_choice map, got %T", result["tool_choice"])
	}
	if tc["type"] != "function" {
		t.Errorf("expected tool_choice type=function, got %v", tc["type"])
	}
}

func TestTranslateAnthropicRequestToOpenAI_ToolChoiceAuto(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-3-opus",
		"tool_choice": map[string]interface{}{
			"type": "auto",
		},
		"messages": []interface{}{},
	}

	result, err := TranslateAnthropicRequestToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["tool_choice"] != "auto" {
		t.Errorf("expected tool_choice=auto, got %v", result["tool_choice"])
	}
}

func TestTranslateAnthropicRequestToOpenAI_ToolChoiceAny(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-3-opus",
		"tool_choice": map[string]interface{}{
			"type": "any",
		},
		"messages": []interface{}{},
	}

	result, err := TranslateAnthropicRequestToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["tool_choice"] != "required" {
		t.Errorf("expected tool_choice=required, got %v", result["tool_choice"])
	}
}

func TestTranslateAnthropicRequestToOpenAI_ToolChoiceNone(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-3-opus",
		"tool_choice": map[string]interface{}{
			"type": "none",
		},
		"messages": []interface{}{},
	}

	result, err := TranslateAnthropicRequestToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["tool_choice"] != "none" {
		t.Errorf("expected tool_choice=none, got %v", result["tool_choice"])
	}
}

func TestTranslateOpenAIResponseToAnthropic_Basic(t *testing.T) {
	data := []byte(`{
		"id": "chatcmpl-123",
		"object": "chat.completion",
		"model": "gpt-4",
		"choices": [
			{
				"index": 0,
				"finish_reason": "stop",
				"message": {
					"role": "assistant",
					"content": "Hello!"
				}
			}
		],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 5,
			"total_tokens": 15
		}
	}`)

	result, err := TranslateOpenAIResponseToAnthropic(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var anthropic map[string]interface{}
	if err := json.Unmarshal(result, &anthropic); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if anthropic["type"] != "message" {
		t.Errorf("expected type=message, got %v", anthropic["type"])
	}
	if anthropic["role"] != "assistant" {
		t.Errorf("expected role=assistant, got %v", anthropic["role"])
	}
	if anthropic["id"] != "123" {
		t.Errorf("expected id=123 (chatcmpl- prefix stripped), got %v", anthropic["id"])
	}
	if anthropic["stop_reason"] != "end_turn" {
		t.Errorf("expected stop_reason=end_turn, got %v", anthropic["stop_reason"])
	}

	usage := anthropic["usage"].(map[string]interface{})
	if usage["input_tokens"] != float64(10) {
		t.Errorf("expected input_tokens=10, got %v", usage["input_tokens"])
	}
	if usage["output_tokens"] != float64(5) {
		t.Errorf("expected output_tokens=5, got %v", usage["output_tokens"])
	}

	content := anthropic["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	textBlock := content[0].(map[string]interface{})
	if textBlock["type"] != "text" {
		t.Errorf("expected type=text, got %v", textBlock["type"])
	}
	if textBlock["text"] != "Hello!" {
		t.Errorf("expected text=Hello!, got %v", textBlock["text"])
	}
}

func TestTranslateOpenAIResponseToAnthropic_WithToolCalls(t *testing.T) {
	data := []byte(`{
		"id": "chatcmpl-456",
		"object": "chat.completion",
		"model": "gpt-4",
		"choices": [
			{
				"index": 0,
				"finish_reason": "tool_calls",
				"message": {
					"role": "assistant",
					"content": null,
					"tool_calls": [
						{
							"id": "call_123",
							"type": "function",
							"function": {
								"name": "get_weather",
								"arguments": "{\"location\":\"NYC\"}"
							}
						}
					]
				}
			}
		],
		"usage": {
			"prompt_tokens": 20,
			"completion_tokens": 10
		}
	}`)

	result, err := TranslateOpenAIResponseToAnthropic(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var anthropic map[string]interface{}
	json.Unmarshal(result, &anthropic)

	if anthropic["stop_reason"] != "tool_use" {
		t.Errorf("expected stop_reason=tool_use, got %v", anthropic["stop_reason"])
	}

	content := anthropic["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	toolBlock := content[0].(map[string]interface{})
	if toolBlock["type"] != "tool_use" {
		t.Errorf("expected type=tool_use, got %v", toolBlock["type"])
	}
	if toolBlock["id"] != "call_123" {
		t.Errorf("expected id=call_123, got %v", toolBlock["id"])
	}
	if toolBlock["name"] != "get_weather" {
		t.Errorf("expected name=get_weather, got %v", toolBlock["name"])
	}
	input := toolBlock["input"].(map[string]interface{})
	if input["location"] != "NYC" {
		t.Errorf("expected input.location=NYC, got %v", input["location"])
	}
}

func TestTranslateOpenAIResponseToAnthropic_WithReasoning(t *testing.T) {
	data := []byte(`{
		"id": "chatcmpl-789",
		"object": "chat.completion",
		"model": "gpt-4",
		"choices": [
			{
				"index": 0,
				"finish_reason": "stop",
				"message": {
					"role": "assistant",
					"content": "The answer is 42.",
					"reasoning": "Let me calculate..."
				}
			}
		],
		"usage": {
			"prompt_tokens": 5,
			"completion_tokens": 3
		}
	}`)

	result, err := TranslateOpenAIResponseToAnthropic(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var anthropic map[string]interface{}
	json.Unmarshal(result, &anthropic)

	content := anthropic["content"].([]interface{})
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks (text + thinking), got %d", len(content))
	}

	thinkingBlock := content[1].(map[string]interface{})
	if thinkingBlock["type"] != "thinking" {
		t.Errorf("expected type=thinking, got %v", thinkingBlock["type"])
	}
	if thinkingBlock["thinking"] != "Let me calculate..." {
		t.Errorf("unexpected thinking text: %v", thinkingBlock["thinking"])
	}
}

func TestTranslateOpenAIResponseToAnthropic_FinishReasonLength(t *testing.T) {
	data := []byte(`{
		"id": "chatcmpl-abc",
		"object": "chat.completion",
		"model": "gpt-4",
		"choices": [
			{
				"index": 0,
				"finish_reason": "length",
				"message": {"role": "assistant", "content": ""}
			}
		],
		"usage": {"prompt_tokens": 1, "completion_tokens": 1}
	}`)

	result, err := TranslateOpenAIResponseToAnthropic(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var anthropic map[string]interface{}
	json.Unmarshal(result, &anthropic)

	if anthropic["stop_reason"] != "max_tokens" {
		t.Errorf("expected stop_reason=max_tokens, got %v", anthropic["stop_reason"])
	}
}

func TestTranslateOpenAIResponseToAnthropic_NoChoices(t *testing.T) {
	// Error response with no choices — should pass through
	data := []byte(`{"error": {"message": "invalid request"}}`)

	result, err := TranslateOpenAIResponseToAnthropic(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return the original data
	if string(result) != string(data) {
		t.Errorf("expected passthrough for no-choices response")
	}
}

func TestMsgOpenAIIDToAnthropic(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"chatcmpl-123", "123"},
		{"chatcmpl-abc-def", "abc-def"},
		{"other-id", "other-id"},
		{"", ""},
	}
	for _, tt := range tests {
		got := msgOpenAIIDToAnthropic(tt.input)
		if got != tt.want {
			t.Errorf("msgOpenAIIDToAnthropic(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
