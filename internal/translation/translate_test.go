package translation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTranslateRequest(t *testing.T) {
	t.Run("nil body", func(t *testing.T) {
		_, err := TranslateRequest(nil, "openai", "anthropic")
		if err == nil {
			t.Fatal("expected error for nil body")
		}
	})

	t.Run("same format", func(t *testing.T) {
		body := map[string]interface{}{
			"model":    "gpt-4",
			"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		}
		result, err := TranslateRequest(body, "openai", "openai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result["model"] != "gpt-4" {
			t.Fatalf("expected model=gpt-4, got %v", result["model"])
		}
		if _, ok := result["max_tokens"]; ok {
			t.Fatal("expected max_tokens to NOT be set for same format")
		}
	})

	t.Run("unsupported translation", func(t *testing.T) {
		body := map[string]interface{}{"model": "foo"}
		_, err := TranslateRequest(body, "foo", "bar")
		if err == nil {
			t.Fatal("expected error for unsupported translation")
		}
	})

	t.Run("openai to anthropic without max_tokens", func(t *testing.T) {
		body := map[string]interface{}{
			"model": "claude-3-opus",
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "hello"},
			},
		}
		result, err := TranslateRequest(body, "openai", "anthropic")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result["max_tokens"] != 4096 {
			t.Fatalf("expected max_tokens=4096, got %v", result["max_tokens"])
		}
	})
}

func TestTranslateRequestAnthropicMaxTokens(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-3-opus",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "hello"},
		},
	}

	result, err := TranslateRequest(body, "openai", "anthropic")
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

	result, err := TranslateRequest(body, "openai", "openai")
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

	result, err := TranslateRequest(body, "openai", "anthropic")
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

	result, err := TranslateRequest(body, "openai", "anthropic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc, ok := result["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool_choice to be translated, got %T", result["tool_choice"])
	}
	if tc["type"] != "auto" {
		t.Fatalf("expected tool_choice type=auto, got %v", tc["type"])
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

func TestTranslateAnthropicToOpenAI(t *testing.T) {
	t.Run("basic response with text content", func(t *testing.T) {
		data := []byte(`{
			"id": "msg_01",
			"type": "message",
			"role": "assistant",
			"model": "claude-3-opus",
			"stop_reason": "end_turn",
			"content": [{"type": "text", "text": "Hello!"}],
			"usage": {"input_tokens": 10, "output_tokens": 5}
		}`)
		result, err := translateAnthropicToOpenAI(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var openai map[string]interface{}
		if err := json.Unmarshal(result, &openai); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}
		choices := openai["choices"].([]interface{})
		msg := choices[0].(map[string]interface{})["message"].(map[string]interface{})
		if msg["content"] != "Hello!" {
			t.Fatalf("expected content=Hello!, got %v", msg["content"])
		}
		if choices[0].(map[string]interface{})["finish_reason"] != "stop" {
			t.Fatalf("expected finish_reason=stop, got %v", choices[0].(map[string]interface{})["finish_reason"])
		}
	})

	t.Run("response with tool_use content", func(t *testing.T) {
		data := []byte(`{
			"id": "msg_02",
			"type": "message",
			"role": "assistant",
			"model": "claude-3-opus",
			"stop_reason": "tool_use",
			"content": [
				{"type": "tool_use", "id": "tool_123", "name": "get_weather", "input": {"location": "NYC"}}
			],
			"usage": {"input_tokens": 20, "output_tokens": 10}
		}`)
		result, err := translateAnthropicToOpenAI(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var openai map[string]interface{}
		if err := json.Unmarshal(result, &openai); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}
		choices := openai["choices"].([]interface{})
		msg := choices[0].(map[string]interface{})["message"].(map[string]interface{})
		toolCalls, ok := msg["tool_calls"].([]interface{})
		if !ok || len(toolCalls) != 1 {
			t.Fatalf("expected 1 tool_call, got %v", msg["tool_calls"])
		}
		tc := toolCalls[0].(map[string]interface{})
		if tc["id"] != "tool_123" {
			t.Fatalf("expected id=tool_123, got %v", tc["id"])
		}
		if choices[0].(map[string]interface{})["finish_reason"] != "tool_calls" {
			t.Fatalf("expected finish_reason=tool_calls, got %v", choices[0].(map[string]interface{})["finish_reason"])
		}
	})

	t.Run("response with thinking content", func(t *testing.T) {
		data := []byte(`{
			"id": "msg_03",
			"type": "message",
			"role": "assistant",
			"model": "claude-3-opus",
			"stop_reason": "end_turn",
			"content": [
				{"type": "text", "text": "The answer is 42."},
				{"type": "thinking", "thinking": "Let me calculate..."}
			],
			"usage": {"input_tokens": 5, "output_tokens": 3}
		}`)
		result, err := translateAnthropicToOpenAI(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var openai map[string]interface{}
		if err := json.Unmarshal(result, &openai); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}
		choices := openai["choices"].([]interface{})
		msg := choices[0].(map[string]interface{})["message"].(map[string]interface{})
		// thinking content is not part of OpenAI message content, but it should not error
		if msg["content"] != "The answer is 42." {
			t.Fatalf("expected content=The answer is 42., got %v", msg["content"])
		}
	})

	t.Run("response with max_tokens stop_reason", func(t *testing.T) {
		data := []byte(`{
			"id": "msg_04",
			"type": "message",
			"role": "assistant",
			"model": "claude-3-opus",
			"stop_reason": "max_tokens",
			"content": [{"type": "text", "text": ""}],
			"usage": {"input_tokens": 1, "output_tokens": 1}
		}`)
		result, err := translateAnthropicToOpenAI(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var openai map[string]interface{}
		if err := json.Unmarshal(result, &openai); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}
		choices := openai["choices"].([]interface{})
		if choices[0].(map[string]interface{})["finish_reason"] != "length" {
			t.Fatalf("expected finish_reason=length, got %v", choices[0].(map[string]interface{})["finish_reason"])
		}
	})

	t.Run("response with usage fields", func(t *testing.T) {
		data := []byte(`{
			"id": "msg_05",
			"type": "message",
			"role": "assistant",
			"model": "claude-3-opus",
			"stop_reason": "end_turn",
			"content": [{"type": "text", "text": "Hi"}],
			"usage": {"input_tokens": 100, "output_tokens": 50}
		}`)
		result, err := translateAnthropicToOpenAI(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var openai map[string]interface{}
		if err := json.Unmarshal(result, &openai); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}
		usage := openai["usage"].(map[string]interface{})
		if usage["prompt_tokens"] != float64(100) {
			t.Fatalf("expected prompt_tokens=100, got %v", usage["prompt_tokens"])
		}
		if usage["completion_tokens"] != float64(50) {
			t.Fatalf("expected completion_tokens=50, got %v", usage["completion_tokens"])
		}
		if usage["total_tokens"] != float64(150) {
			t.Fatalf("expected total_tokens=150, got %v", usage["total_tokens"])
		}
	})

	t.Run("invalid JSON input", func(t *testing.T) {
		data := []byte(`{invalid json`)
		_, err := translateAnthropicToOpenAI(data)
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})
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

func TestConvertMessages(t *testing.T) {
	t.Run("system message as string", func(t *testing.T) {
		body := map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"role": "system", "content": "Be helpful."},
				map[string]interface{}{"role": "user", "content": "hello"},
			},
		}
		if err := convertMessages(body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if body["system"] != "Be helpful." {
			t.Fatalf("expected system field, got %v", body["system"])
		}
		msgs := body["messages"].([]interface{})
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message, got %d", len(msgs))
		}
	})

	t.Run("system message as array", func(t *testing.T) {
		body := map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{
					"role": "system",
					"content": []interface{}{
						map[string]interface{}{"type": "text", "text": "Be helpful."},
						map[string]interface{}{"type": "text", "text": "Be concise."},
					},
				},
				map[string]interface{}{"role": "user", "content": "hello"},
			},
		}
		if err := convertMessages(body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		sys := body["system"].([]map[string]interface{})
		if len(sys) != 2 {
			t.Fatalf("expected 2 system parts, got %d", len(sys))
		}
	})

	t.Run("tool message with tool_call_id", func(t *testing.T) {
		body := map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"role": "tool", "content": `{"temp":72}`, "tool_call_id": "call_123"},
			},
		}
		if err := convertMessages(body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		msgs := body["messages"].([]interface{})
		msg := msgs[0].(map[string]interface{})
		if msg["role"] != "user" {
			t.Fatalf("expected role=user, got %v", msg["role"])
		}
		content := msg["content"].([]map[string]interface{})
		if content[0]["type"] != "tool_result" {
			t.Fatalf("expected tool_result, got %v", content[0]["type"])
		}
		if content[0]["tool_use_id"] != "call_123" {
			t.Fatalf("expected tool_use_id=call_123, got %v", content[0]["tool_use_id"])
		}
	})

	t.Run("tool message without tool_call_id", func(t *testing.T) {
		body := map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"role": "tool", "content": `{"temp":72}`},
			},
		}
		if err := convertMessages(body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		msgs := body["messages"].([]interface{})
		msg := msgs[0].(map[string]interface{})
		if msg["role"] != "user" {
			t.Fatalf("expected role=user, got %v", msg["role"])
		}
		if msg["content"] != `Tool result: {"temp":72}` {
			t.Fatalf("expected prefixed content, got %v", msg["content"])
		}
	})

	t.Run("assistant message with tool_calls", func(t *testing.T) {
		body := map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{
					"role":    "assistant",
					"content": "Let me check.",
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id": "call_123",
							"function": map[string]interface{}{
								"name":      "get_weather",
								"arguments": `{"location":"NYC"}`,
							},
						},
					},
				},
			},
		}
		if err := convertMessages(body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		msgs := body["messages"].([]interface{})
		msg := msgs[0].(map[string]interface{})
		if msg["role"] != "assistant" {
			t.Fatalf("expected role=assistant, got %v", msg["role"])
		}
		content := msg["content"].([]map[string]interface{})
		if len(content) != 2 {
			t.Fatalf("expected 2 content blocks, got %d", len(content))
		}
		if content[0]["type"] != "text" {
			t.Fatalf("expected first block text, got %v", content[0]["type"])
		}
		if content[1]["type"] != "tool_use" {
			t.Fatalf("expected second block tool_use, got %v", content[1]["type"])
		}
	})

	t.Run("assistant message with array content", func(t *testing.T) {
		body := map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{
					"role": "assistant",
					"content": []interface{}{
						map[string]interface{}{"type": "text", "text": "hello"},
						map[string]interface{}{"type": "image_url", "image_url": "data:image/png;base64,abc"},
					},
				},
			},
		}
		if err := convertMessages(body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		msgs := body["messages"].([]interface{})
		msg := msgs[0].(map[string]interface{})
		content := msg["content"].([]interface{})
		if len(content) != 2 {
			t.Fatalf("expected 2 content blocks, got %d", len(content))
		}
	})

	t.Run("user message with array content", func(t *testing.T) {
		body := map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{
					"role": "user",
					"content": []interface{}{
						map[string]interface{}{"type": "text", "text": "hello"},
						map[string]interface{}{"type": "image_url", "image_url": "data:image/png;base64,abc"},
					},
				},
			},
		}
		if err := convertMessages(body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		msgs := body["messages"].([]interface{})
		msg := msgs[0].(map[string]interface{})
		if msg["role"] != "user" {
			t.Fatalf("expected role=user, got %v", msg["role"])
		}
		content := msg["content"].([]interface{})
		if len(content) != 2 {
			t.Fatalf("expected 2 content blocks, got %d", len(content))
		}
	})

	t.Run("non-map message in array", func(t *testing.T) {
		body := map[string]interface{}{
			"messages": []interface{}{
				"invalid-message",
				map[string]interface{}{"role": "user", "content": "hello"},
			},
		}
		if err := convertMessages(body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		msgs := body["messages"].([]interface{})
		if len(msgs) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(msgs))
		}
		if msgs[0] != "invalid-message" {
			t.Fatalf("expected non-map message preserved, got %v", msgs[0])
		}
	})

	t.Run("multiple system messages", func(t *testing.T) {
		body := map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"role": "system", "content": "First system."},
				map[string]interface{}{"role": "system", "content": "Second system."},
				map[string]interface{}{"role": "user", "content": "hello"},
			},
		}
		if err := convertMessages(body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		sys := body["system"].([]map[string]interface{})
		if len(sys) != 2 {
			t.Fatalf("expected 2 system parts, got %d", len(sys))
		}
		if sys[0]["text"] != "First system." {
			t.Fatalf("expected first system text, got %v", sys[0]["text"])
		}
		if sys[1]["text"] != "Second system." {
			t.Fatalf("expected second system text, got %v", sys[1]["text"])
		}
	})
}

func TestConvertOpenAIContentBlocks(t *testing.T) {
	// image_url as string
	blocks := []interface{}{
		map[string]interface{}{
			"type":      "image_url",
			"image_url": "data:image/png;base64,abc123",
		},
	}
	result := convertOpenAIContentBlocks(blocks)
	if len(result) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result))
	}
	img, ok := result[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", result[0])
	}
	if img["type"] != "image" {
		t.Errorf("expected type=image, got %v", img["type"])
	}
	src, ok := img["source"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected source map, got %T", img["source"])
	}
	if src["data"] != "abc123" {
		t.Errorf("expected data=abc123, got %v", src["data"])
	}
	if src["media_type"] != "image/png" {
		t.Errorf("expected media_type=image/png, got %v", src["media_type"])
	}

	// image_url as map with url field
	blocks = []interface{}{
		map[string]interface{}{
			"type": "image_url",
			"image_url": map[string]interface{}{
				"url": "data:image/jpeg;base64,def456",
			},
		},
	}
	result = convertOpenAIContentBlocks(blocks)
	if len(result) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result))
	}
	src = result[0].(map[string]interface{})["source"].(map[string]interface{})
	if src["data"] != "def456" {
		t.Errorf("expected data=def456, got %v", src["data"])
	}
	if src["media_type"] != "image/jpeg" {
		t.Errorf("expected media_type=image/jpeg, got %v", src["media_type"])
	}

	// text block
	blocks = []interface{}{
		map[string]interface{}{
			"type": "text",
			"text": "hello world",
		},
	}
	result = convertOpenAIContentBlocks(blocks)
	if len(result) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result))
	}
	txt, ok := result[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", result[0])
	}
	if txt["type"] != "text" {
		t.Errorf("expected type=text, got %v", txt["type"])
	}
	if txt["text"] != "hello world" {
		t.Errorf("expected text=hello world, got %v", txt["text"])
	}

	// unknown block should pass through
	blocks = []interface{}{
		map[string]interface{}{
			"type": "custom",
			"data": "value",
		},
	}
	result = convertOpenAIContentBlocks(blocks)
	if len(result) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result))
	}
	unk, ok := result[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", result[0])
	}
	if unk["type"] != "custom" {
		t.Errorf("expected type=custom, got %v", unk["type"])
	}
	if unk["data"] != "value" {
		t.Errorf("expected data=value, got %v", unk["data"])
	}

	// non-map block should be skipped
	blocks = []interface{}{"invalid"}
	result = convertOpenAIContentBlocks(blocks)
	if len(result) != 0 {
		t.Errorf("expected 0 blocks for non-map input, got %d", len(result))
	}
}

func TestConvertToolChoice(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		expected map[string]interface{}
		deleted  bool
	}{
		{
			name:     "string auto",
			input:    map[string]interface{}{"tool_choice": "auto"},
			expected: map[string]interface{}{"type": "auto"},
		},
		{
			name:     "string required",
			input:    map[string]interface{}{"tool_choice": "required"},
			expected: map[string]interface{}{"type": "any"},
		},
		{
			name:     "string any",
			input:    map[string]interface{}{"tool_choice": "any"},
			expected: map[string]interface{}{"type": "any"},
		},
		{
			name:     "string none",
			input:    map[string]interface{}{"tool_choice": "none"},
			expected: map[string]interface{}{"type": "none"},
		},
		{
			name:     "string unknown defaults to auto",
			input:    map[string]interface{}{"tool_choice": "unknown"},
			expected: map[string]interface{}{"type": "auto"},
		},
		{
			name: "map with function type and name",
			input: map[string]interface{}{
				"tool_choice": map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name": "get_weather",
					},
				},
			},
			expected: map[string]interface{}{"type": "tool", "name": "get_weather"},
		},
		{
			name: "map without function name left as-is",
			input: map[string]interface{}{
				"tool_choice": map[string]interface{}{
					"type": "function",
				},
			},
			expected: map[string]interface{}{"type": "function"},
		},
		{
			name:    "non-string non-map deleted",
			input:   map[string]interface{}{"tool_choice": 42},
			deleted: true,
		},
		{
			name:    "missing tool_choice no-op",
			input:   map[string]interface{}{"model": "gpt-4"},
			deleted: true, // tool_choice key is absent, which we verify separately
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := make(map[string]interface{})
			for k, v := range tt.input {
				body[k] = v
			}
			convertToolChoice(body)
			if tt.deleted {
				if _, ok := body["tool_choice"]; ok {
					t.Fatalf("expected tool_choice to be deleted, got %v", body["tool_choice"])
				}
				return
			}
			got, ok := body["tool_choice"].(map[string]interface{})
			if !ok {
				t.Fatalf("expected map, got %T", body["tool_choice"])
			}
			for k, v := range tt.expected {
				if got[k] != v {
					t.Errorf("expected %s=%v, got %v", k, v, got[k])
				}
			}
		})
	}
}

func TestConvertTools(t *testing.T) {
	t.Run("empty tools array", func(t *testing.T) {
		body := map[string]interface{}{
			"tools": []interface{}{},
		}
		convertTools(body)
		if _, ok := body["tools"]; ok {
			t.Fatal("expected tools to be deleted for empty array")
		}
	})

	t.Run("tool with valid input_schema", func(t *testing.T) {
		body := map[string]interface{}{
			"tools": []interface{}{
				map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name":        "get_weather",
						"description": "Get weather info",
						"parameters":  map[string]interface{}{"type": "object"},
					},
				},
			},
		}
		convertTools(body)
		tools, ok := body["tools"].([]interface{})
		if !ok || len(tools) != 1 {
			t.Fatalf("expected 1 tool, got %v", body["tools"])
		}
		tool := tools[0].(map[string]interface{})
		if tool["name"] != "get_weather" {
			t.Errorf("expected name=get_weather, got %v", tool["name"])
		}
		if tool["description"] != "Get weather info" {
			t.Errorf("expected description, got %v", tool["description"])
		}
		if tool["input_schema"] == nil {
			t.Error("expected input_schema to be set")
		}
	})

	t.Run("tool with nil properties in input_schema", func(t *testing.T) {
		body := map[string]interface{}{
			"tools": []interface{}{
				map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name":        "get_weather",
						"description": "Get weather info",
						"parameters":  nil,
					},
				},
			},
		}
		convertTools(body)
		tools, ok := body["tools"].([]interface{})
		if !ok || len(tools) != 1 {
			t.Fatalf("expected 1 tool, got %v", body["tools"])
		}
		tool := tools[0].(map[string]interface{})
		if tool["name"] != "get_weather" {
			t.Errorf("expected name=get_weather, got %v", tool["name"])
		}
		if _, ok := tool["input_schema"]; ok {
			t.Error("expected input_schema to be absent when parameters is nil")
		}
	})

	t.Run("non-map tool entry skipped", func(t *testing.T) {
		body := map[string]interface{}{
			"tools": []interface{}{
				"invalid-tool",
				map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name": "valid_tool",
					},
				},
			},
		}
		convertTools(body)
		tools, ok := body["tools"].([]interface{})
		if !ok || len(tools) != 1 {
			t.Fatalf("expected 1 tool after filtering, got %v", body["tools"])
		}
		tool := tools[0].(map[string]interface{})
		if tool["name"] != "valid_tool" {
			t.Errorf("expected name=valid_tool, got %v", tool["name"])
		}
	})

	t.Run("tool missing name sets nil name", func(t *testing.T) {
		body := map[string]interface{}{
			"tools": []interface{}{
				map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"description": "No name tool",
					},
				},
			},
		}
		convertTools(body)
		tools, ok := body["tools"].([]interface{})
		if !ok || len(tools) != 1 {
			t.Fatalf("expected 1 tool, got %v", body["tools"])
		}
		tool := tools[0].(map[string]interface{})
		if tool["name"] != nil {
			t.Errorf("expected nil name, got %v", tool["name"])
		}
	})

	t.Run("tool missing description sets empty string", func(t *testing.T) {
		body := map[string]interface{}{
			"tools": []interface{}{
				map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name":       "get_weather",
						"parameters": map[string]interface{}{"type": "object"},
					},
				},
			},
		}
		convertTools(body)
		tools, ok := body["tools"].([]interface{})
		if !ok || len(tools) != 1 {
			t.Fatalf("expected 1 tool, got %v", body["tools"])
		}
		tool := tools[0].(map[string]interface{})
		if _, ok := tool["description"]; ok {
			t.Error("expected description to be absent when not provided")
		}
	})
}

func TestConvertOpenAIToAnthropic(t *testing.T) {
	t.Run("body with no messages key", func(t *testing.T) {
		body := map[string]interface{}{
			"model": "gpt-4",
		}
		err := convertOpenAIToAnthropic(body)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if body["model"] != "gpt-4" {
			t.Errorf("expected model preserved, got %v", body["model"])
		}
	})

	t.Run("body with max_tokens already set", func(t *testing.T) {
		body := map[string]interface{}{
			"model":      "gpt-4",
			"max_tokens": 2048,
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "hi"},
			},
		}
		result, err := TranslateRequest(body, "openai", "anthropic")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result["max_tokens"] != 2048 {
			t.Errorf("expected max_tokens=2048 preserved, got %v", result["max_tokens"])
		}
	})

	t.Run("body with tools calls convertTools", func(t *testing.T) {
		body := map[string]interface{}{
			"model": "gpt-4",
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "hi"},
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
		}
		err := convertOpenAIToAnthropic(body)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tools, ok := body["tools"].([]interface{})
		if !ok || len(tools) != 1 {
			t.Fatalf("expected 1 tool, got %v", body["tools"])
		}
		tool := tools[0].(map[string]interface{})
		if tool["name"] != "get_weather" {
			t.Errorf("expected tool name converted, got %v", tool["name"])
		}
	})

	t.Run("body with tool_choice calls convertToolChoice", func(t *testing.T) {
		body := map[string]interface{}{
			"model": "gpt-4",
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "hi"},
			},
			"tool_choice": "auto",
		}
		err := convertOpenAIToAnthropic(body)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tc, ok := body["tool_choice"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected tool_choice map, got %T", body["tool_choice"])
		}
		if tc["type"] != "auto" {
			t.Errorf("expected tool_choice type=auto, got %v", tc["type"])
		}
	})
}

func TestTranslateResponse(t *testing.T) {
	t.Run("same format returns unchanged", func(t *testing.T) {
		data := []byte(`{"id":"chatcmpl-123"}`)
		result, err := TranslateResponse(data, "openai", "openai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(result) != string(data) {
			t.Errorf("expected unchanged data, got %s", string(result))
		}
	})

	t.Run("openai to anthropic with invalid JSON returns error", func(t *testing.T) {
		data := []byte(`{invalid json`)
		_, err := TranslateResponse(data, "anthropic", "openai")
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})

	t.Run("openai to anthropic with invalid JSON returns error", func(t *testing.T) {
		data := []byte(`{invalid json`)
		_, err := TranslateResponse(data, "openai", "anthropic")
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})
}

func TestAnthropicToOpenAIStream(t *testing.T) {
	t.Run("SSE event with tool_use block", func(t *testing.T) {
		input := `data: {"type":"message_start","message":{"id":"msg_01","model":"claude-3"}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_123","name":"get_weather"}}

data: {"type":"content_block_delta","index":0,"delta":{"partial_json":"{\"loc\":\"NYC\"}"}}

data: {"type":"message_stop"}

`
		out := anthropicToOpenAIStream(strings.NewReader(input))
		lines := readAllLines(t, out)

		var foundToolCalls []map[string]interface{}
		for _, line := range lines {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			event := parseDataLine(t, line)
			choices, _ := event["choices"].([]interface{})
			if len(choices) > 0 {
				choice := choices[0].(map[string]interface{})
				delta, _ := choice["delta"].(map[string]interface{})
				if tcs, ok := delta["tool_calls"].([]interface{}); ok {
					for _, tc := range tcs {
						foundToolCalls = append(foundToolCalls, tc.(map[string]interface{}))
					}
				}
			}
		}
		if len(foundToolCalls) < 2 {
			t.Fatalf("expected at least 2 tool_call chunks, got %d", len(foundToolCalls))
		}
		if foundToolCalls[0]["id"] != "tool_123" {
			t.Errorf("expected id=tool_123, got %v", foundToolCalls[0]["id"])
		}
	})

	t.Run("SSE event with thinking block", func(t *testing.T) {
		input := `data: {"type":"message_start","message":{"id":"msg_01","model":"claude-3"}}

data: {"type":"thinking_delta","delta":{"thinking":"Let me think..."}}

data: {"type":"message_stop"}

`
		out := anthropicToOpenAIStream(strings.NewReader(input))
		lines := readAllLines(t, out)

		var foundThinking string
		for _, line := range lines {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			event := parseDataLine(t, line)
			choices, _ := event["choices"].([]interface{})
			if len(choices) > 0 {
				choice := choices[0].(map[string]interface{})
				delta, _ := choice["delta"].(map[string]interface{})
				if reasoning, ok := delta["reasoning"].(string); ok {
					foundThinking = reasoning
				}
			}
		}
		if foundThinking != "Let me think..." {
			t.Errorf("expected thinking preserved, got %q", foundThinking)
		}
	})

	t.Run("invalid JSON line skipped gracefully", func(t *testing.T) {
		input := `data: {invalid json}

data: {"type":"message_stop"}

`
		out := anthropicToOpenAIStream(strings.NewReader(input))
		lines := readAllLines(t, out)
		if len(lines) == 0 {
			t.Fatal("expected some output lines")
		}
		foundDone := false
		for _, line := range lines {
			if line == "data: [DONE]" {
				foundDone = true
				break
			}
		}
		if !foundDone {
			t.Fatalf("expected [DONE] in output, got lines: %v", lines)
		}
	})

	t.Run("empty line skipped", func(t *testing.T) {
		input := `data: {"type":"message_stop"}



`
		out := anthropicToOpenAIStream(strings.NewReader(input))
		lines := readAllLines(t, out)
		if len(lines) == 0 {
			t.Fatal("expected output lines")
		}
	})
}

func TestOpenAIToAnthropicStream(t *testing.T) {
	t.Run("SSE event with tool_calls converted to tool_use", func(t *testing.T) {
		input := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"loc\":\"NYC\"}"}}]}}]}

data: [DONE]

`
		out := OpenAIToAnthropicStream(strings.NewReader(input))
		lines := readAllLines(t, out)

		var foundTypes []string
		for _, line := range lines {
			if strings.HasPrefix(line, "event: ") {
				foundTypes = append(foundTypes, strings.TrimPrefix(line, "event: "))
			}
		}
		if len(foundTypes) < 3 {
			t.Fatalf("expected at least 3 events, got %d: %v", len(foundTypes), foundTypes)
		}
		if foundTypes[1] != "content_block_start" {
			t.Errorf("expected content_block_start, got %q", foundTypes[1])
		}
		if foundTypes[2] != "content_block_delta" {
			t.Errorf("expected content_block_delta, got %q", foundTypes[2])
		}
	})

	t.Run("SSE event with function_call converted to tool_use", func(t *testing.T) {
		input := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}

data: [DONE]

`
		out := OpenAIToAnthropicStream(strings.NewReader(input))
		lines := readAllLines(t, out)

		var foundBlockStart bool
		for _, line := range lines {
			if strings.TrimSpace(line) == "event: content_block_start" {
				foundBlockStart = true
				break
			}
		}
		if !foundBlockStart {
			t.Fatal("expected content_block_start event")
		}
	})

	t.Run("invalid JSON skipped", func(t *testing.T) {
		input := `data: {invalid json}

data: [DONE]

`
		out := OpenAIToAnthropicStream(strings.NewReader(input))
		lines := readAllLines(t, out)
		// No valid chunks were received, so no message_start was emitted;
		// message_stop should not be sent without a preceding message_start.
		if len(lines) != 0 {
			t.Fatalf("expected no events for empty stream, got lines: %v", lines)
		}
	})
}

func TestStringify(t *testing.T) {
	// string input
	if got := stringify("hello"); got != "hello" {
		t.Errorf("stringify(string) = %q, want %q", got, "hello")
	}

	// map input should JSON marshal
	m := map[string]interface{}{"key": "value", "num": 42}
	got := stringify(m)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("stringify(map) did not produce valid JSON: %q", got)
	}
	if parsed["key"] != "value" {
		t.Errorf("expected key=value, got %v", parsed["key"])
	}

	// unmarshalable input should fall back to fmt.Sprint
	bad := make(chan int)
	got = stringify(bad)
	if got == "" {
		t.Error("expected non-empty fallback string for unmarshalable input")
	}
}
