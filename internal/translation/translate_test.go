package translation

import (
	"encoding/json"
	"testing"
)

func TestTranslateRequestAnthropicMaxTokens(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-3-opus",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "hello"},
		},
	}

	result, err := TranslateRequest(body, "anthropic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result["max_tokens"] == nil {
		t.Fatal("expected max_tokens to be set")
	}

	if result["max_tokens"] != 4096 {
		t.Fatalf("expected max_tokens=4096, got %v", result["max_tokens"])
	}

	// Ensure other fields are preserved
	if result["model"] != "claude-3-opus" {
		t.Fatalf("expected model to be preserved, got %v", result["model"])
	}
}

func TestTranslateRequestOpenAIPassthrough(t *testing.T) {
	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
	}

	result, err := TranslateRequest(body, "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result["model"] != "gpt-4" {
		t.Fatalf("expected model=gpt-4, got %v", result["model"])
	}

	// max_tokens should NOT be added for openai
	if _, ok := result["max_tokens"]; ok {
		t.Fatal("expected max_tokens to NOT be set for openai target")
	}
}

func TestTranslateResponseSameFormat(t *testing.T) {
	data := []byte(`{"id":"chatcmpl-123","object":"chat.completion"}`)

	result, err := TranslateResponse(data, "openai", "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(result) != string(data) {
		t.Fatalf("expected passthrough, got %s", string(result))
	}
}

func TestTranslateRequestAnthropicSystemMessage(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-3-opus",
		"messages": []interface{}{
			map[string]interface{}{"role": "system", "content": "You are a helpful assistant."},
			map[string]interface{}{"role": "user", "content": "hello"},
		},
	}

	result, err := TranslateRequest(body, "anthropic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result["system"] != "You are a helpful assistant." {
		t.Fatalf("expected system field, got %v", result["system"])
	}

	msgs, ok := result["messages"].([]interface{})
	if !ok {
		t.Fatalf("expected messages to be array, got %T", result["messages"])
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message after removing system, got %d", len(msgs))
	}

	msg := msgs[0].(map[string]interface{})
	if msg["role"] != "user" {
		t.Fatalf("expected user role, got %v", msg["role"])
	}
}

func TestTranslateRequestAnthropicTools(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-3-opus",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "What's the weather?"},
			map[string]interface{}{
				"role": "assistant",
				"content": "Let me check.",
				"tool_calls": []interface{}{
					map[string]interface{}{
						"type": "function",
						"function": map[string]interface{}{
							"name": "get_weather", "arguments": `{"location":"NYC"}`,
						},
					},
				},
			},
			map[string]interface{}{"role": "tool", "content": `{"temp":72}`, "tool_call_id": "1"},
		},
		"tools": []interface{}{
			map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        "get_weather",
					"description": "Get weather",
					"parameters":  map[string]interface{}{"type": "object"},
				},
			},
		},
		"tool_choice": "auto",
	}

	result, err := TranslateRequest(body, "anthropic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := result["tool_choice"]; ok {
		t.Fatal("expected tool_choice to be removed")
	}

	tools, ok := result["tools"].([]interface{})
	if !ok {
		t.Fatalf("expected tools to be array, got %T", result["tools"])
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]interface{})
	if tool["name"] != "get_weather" {
		t.Fatalf("expected tool name get_weather, got %v", tool["name"])
	}
	if tool["input_schema"] == nil {
		t.Fatal("expected input_schema to be set")
	}

	msgs, ok := result["messages"].([]interface{})
	if !ok {
		t.Fatalf("expected messages to be array, got %T", result["messages"])
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	// Tool result converted to user
	if msgs[2].(map[string]interface{})["role"] != "user" {
		t.Fatalf("expected tool message converted to user, got %v", msgs[2].(map[string]interface{})["role"])
	}
}

func TestTranslateResponseAnthropicToOpenAI(t *testing.T) {
	data := []byte(`{
		"id": "msg_01",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-opus",
		"stop_reason": "end_turn",
		"content": [{"type": "text", "text": "Hello!"}],
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)

	result, err := TranslateResponse(data, "anthropic", "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]interface{}
	if err := json.Unmarshal(result, &openai); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if openai["object"] != "chat.completion" {
		t.Fatalf("expected object=chat.completion, got %v", openai["object"])
	}

	choices, ok := openai["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		t.Fatal("expected choices array")
	}

	msg := choices[0].(map[string]interface{})["message"].(map[string]interface{})
	if msg["content"] != "Hello!" {
		t.Fatalf("expected content=Hello!, got %v", msg["content"])
	}

	usage := openai["usage"].(map[string]interface{})
	if usage["prompt_tokens"] != float64(10) {
		t.Fatalf("expected prompt_tokens=10, got %v", usage["prompt_tokens"])
	}
	if usage["completion_tokens"] != float64(5) {
		t.Fatalf("expected completion_tokens=5, got %v", usage["completion_tokens"])
	}
}
