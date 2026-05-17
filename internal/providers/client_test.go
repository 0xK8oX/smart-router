package providers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"smart-router/internal/types"
)

func TestCallProvider(t *testing.T) {
	// Start httptest server that returns OpenAI-style response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("expected path /v1/chat/completions, got %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("expected Authorization Bearer sk-test, got %s", auth)
		}

		// Verify request body
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("failed to unmarshal request body: %v", err)
		}
		if reqBody["model"] != "gpt-4" {
			t.Errorf("expected model gpt-4, got %v", reqBody["model"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test","object":"chat.completion","choices":[]}`))
	}))
	defer server.Close()

	client := NewClient()
	provider := types.ProviderConfig{
		Name:    "openai",
		BaseURL: server.URL,
		Model:   "gpt-4",
		Format:  "openai",
		Timeout: 5,
		APIKey:  "sk-test",
	}
	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, err := client.Call(provider, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestCallProviderAnthropicHeader(t *testing.T) {
	var receivedVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test","type":"message"}`))
	}))
	defer server.Close()

	client := NewClient()
	provider := types.ProviderConfig{
		Name:    "anthropic",
		BaseURL: server.URL,
		Model:   "claude-3-opus",
		Format:  "anthropic",
		Timeout: 5,
		APIKey:  "sk-ant-test",
	}
	body := map[string]interface{}{
		"model":    "claude-3-opus",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, err := client.Call(provider, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if receivedVersion != "2023-06-01" {
		t.Errorf("expected anthropic-version header 2023-06-01, got %s", receivedVersion)
	}
}

func TestCallProviderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient()
	provider := types.ProviderConfig{
		Name:    "slow",
		BaseURL: server.URL,
		Model:   "gpt-4",
		Format:  "openai",
		Timeout: 0, // 0 second timeout — should immediately expire
		APIKey:  "sk-test",
	}
	body := map[string]interface{}{"model": "gpt-4"}

	resp, err := client.Call(provider, body)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected timeout error, got nil")
	}
}

func TestCallStream(t *testing.T) {
	var receivedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer server.Close()

	client := NewClient()
	provider := types.ProviderConfig{
		Name:    "openai",
		BaseURL: server.URL,
		Model:   "gpt-4",
		Format:  "openai",
		Timeout: 5,
		APIKey:  "sk-test",
	}
	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, err := client.CallStream(provider, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	if stream, ok := receivedBody["stream"].(bool); !ok || !stream {
		t.Errorf("expected stream=true in body, got %v", receivedBody["stream"])
	}
}
