package translation

import (
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
