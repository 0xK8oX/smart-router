package providers

import (
	"context"
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

	resp, err := client.Call(context.Background(),provider, body, nil)
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

	resp, err := client.Call(context.Background(),provider, body, nil)
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
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient()
	provider := types.ProviderConfig{
		Name:    "slow",
		BaseURL: server.URL,
		Model:   "gpt-4",
		Format:  "openai",
		Timeout: 1, // 1 second timeout — server is slower, should expire
		APIKey:  "sk-test",
	}
	body := map[string]interface{}{"model": "gpt-4"}

	resp, err := client.Call(context.Background(), provider, body, nil)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected timeout error, got nil")
	}
}

func TestIsNativeAnthropic(t *testing.T) {
	tests := []struct {
		url      string
		expected bool
	}{
		{"https://api.anthropic.com/v1", true},
		{"https://api.anthropic.com", true},
		{"https://api.anthropic.com:443/v1/messages", true},
		{"http://api.anthropic.com", true},
		{"https://proxy.example.com/api.anthropic.com", false},
		{"https://example.com", false},
		{"", false},
		{"not-a-url", false},
	}
	for _, tc := range tests {
		got := isNativeAnthropic(tc.url)
		if got != tc.expected {
			t.Errorf("isNativeAnthropic(%q) = %v, want %v", tc.url, got, tc.expected)
		}
	}
}

func TestDoRequest_KimiCodingHeaders(t *testing.T) {
	var gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer server.Close()

	client := NewClient()
	provider := types.ProviderConfig{
		Name:    "kimi-coding",
		BaseURL: server.URL + "/api.kimi.com/coding",
		Model:   "kimi-coder",
		Format:  "openai",
		Timeout: 5,
		APIKey:  "sk-kimi-test",
	}
	body := map[string]interface{}{"model": "kimi-coder"}

	resp, err := client.Call(context.Background(),provider, body, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if gotUA != "claude-code/0.1.0" {
		t.Errorf("expected User-Agent claude-code/0.1.0, got %s", gotUA)
	}
}

func TestDoRequest_HeaderForwarding(t *testing.T) {
	var gotCustom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCustom = r.Header.Get("X-Custom-Header")
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
	body := map[string]interface{}{"model": "gpt-4"}
	headers := http.Header{}
	headers.Set("X-Custom-Header", "custom-value")

	resp, err := client.Call(context.Background(),provider, body, headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if gotCustom != "custom-value" {
		t.Errorf("expected X-Custom-Header custom-value, got %s", gotCustom)
	}
}

func TestDoRequest_DefaultAuthHeader(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer server.Close()

	client := NewClient()
	provider := types.ProviderConfig{
		Name:    "generic",
		BaseURL: server.URL,
		Model:   "model-x",
		Format:  "openai",
		Timeout: 5,
		APIKey:  "sk-generic-test",
	}
	body := map[string]interface{}{"model": "model-x"}

	resp, err := client.Call(context.Background(),provider, body, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if gotAuth != "Bearer sk-generic-test" {
		t.Errorf("expected Authorization Bearer sk-generic-test, got %s", gotAuth)
	}
}

func TestDoRequest_AnthropicFormatNonNative(t *testing.T) {
	var gotVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer server.Close()

	client := NewClient()
	provider := types.ProviderConfig{
		Name:    "anthropic-proxy",
		BaseURL: server.URL,
		Model:   "claude-3-opus",
		Format:  "anthropic",
		Timeout: 5,
		APIKey:  "sk-proxy-test",
	}
	body := map[string]interface{}{"model": "claude-3-opus"}

	resp, err := client.Call(context.Background(),provider, body, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if gotVersion != "2023-06-01" {
		t.Errorf("expected anthropic-version header 2023-06-01, got %s", gotVersion)
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

	resp, err := client.CallStream(context.Background(),provider, body, nil)
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

func TestDoRequest_ContentLengthStrippedWhenBodyChanges(t *testing.T) {
	var receivedContentLength string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentLength = r.Header.Get("Content-Length")
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

	// Original request had Content-Length for a smaller body
	headers := http.Header{}
	headers.Set("Content-Length", "42")

	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "this is a much longer message that exceeds forty-two bytes easily"}},
	}

	resp, err := client.Call(context.Background(),provider, body, headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	// Go's http.Client recomputes Content-Length from the body reader.
	// The forwarded "42" must have been stripped, or the body would be truncated.
	// We assert that the received length matches the actual marshaled body.
	if receivedContentLength == "42" {
		t.Fatal("Content-Length was forwarded as 42 but body was re-marshaled; upstream would read wrong number of bytes")
	}
}

func TestDoRequest_HopByHopHeadersStripped(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
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

	headers := http.Header{}
	headers.Set("Host", "router.example.com")
	headers.Set("Cookie", "session=abc123")
	headers.Set("Referer", "https://internal-dashboard.example.com")
	headers.Set("Connection", "keep-alive")
	headers.Set("Keep-Alive", "timeout=5")
	headers.Set("Upgrade", "h2c")
	headers.Set("Accept-Encoding", "gzip")
	headers.Set("Transfer-Encoding", "chunked")
	headers.Set("X-Safe-Header", "should-passthrough")

	body := map[string]interface{}{"model": "gpt-4"}

	resp, err := client.Call(context.Background(),provider, body, headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	forbidden := []string{"Host", "Cookie", "Referer", "Connection", "Keep-Alive", "Upgrade", "Transfer-Encoding"}
	for _, h := range forbidden {
		if gotHeaders.Get(h) != "" {
			t.Errorf("hop-by-hop/header %q should have been stripped, got %q", h, gotHeaders.Get(h))
		}
	}
	// Accept-Encoding may be auto-added by Go's transport; we only care that
	// the client's forwarded value was stripped (which it was, via Del).

	if gotHeaders.Get("X-Safe-Header") != "should-passthrough" {
		t.Errorf("safe header X-Safe-Header should have been forwarded, got %q", gotHeaders.Get("X-Safe-Header"))
	}
}

func TestDoRequest_AuthHeaderNotForwarded(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
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
		APIKey:  "sk-provider-key",
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer sr-client-key-must-not-leak")

	body := map[string]interface{}{"model": "gpt-4"}

	resp, err := client.Call(context.Background(),provider, body, headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	// The client's Authorization header should be replaced by the provider's key
	if gotAuth == "Bearer sr-client-key-must-not-leak" {
		t.Fatal("client Authorization header leaked to upstream; expected provider key instead")
	}
	if gotAuth != "Bearer sk-provider-key" {
		t.Errorf("expected provider auth header, got %q", gotAuth)
	}
}
