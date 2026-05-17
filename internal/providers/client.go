package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"smart-router/internal/types"
)

var versionSuffix = regexp.MustCompile(`/v\d+$`)

// Client is an HTTP client for calling upstream LLM providers.
type Client struct {
	httpClient *http.Client
}

// NewClient creates a new provider HTTP client.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{},
	}
}

func buildEndpoint(baseURL string, format string) string {
	base := versionSuffix.ReplaceAllString(baseURL, "")
	if format == "anthropic" {
		return base + "/v1/messages"
	}
	return base + "/v1/chat/completions"
}

func isKimiCodingEndpoint(baseURL string) bool {
	return bytes.Contains([]byte(baseURL), []byte("api.kimi.com")) && bytes.Contains([]byte(baseURL), []byte("/coding"))
}

func isNativeAnthropic(baseURL string) bool {
	return bytes.Contains([]byte(baseURL), []byte("api.anthropic.com"))
}

// Call makes a non-streaming request to the provider.
func (c *Client) Call(provider types.ProviderConfig, body map[string]interface{}) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(provider.Timeout)*time.Second)
	defer cancel()

	endpoint := buildEndpoint(provider.BaseURL, provider.Format)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	if isNativeAnthropic(provider.BaseURL) {
		req.Header.Set("x-api-key", provider.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if isKimiCodingEndpoint(provider.BaseURL) {
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
		req.Header.Set("User-Agent", "claude-code/0.1.0")
	} else {
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}

	if provider.Format == "anthropic" && !isNativeAnthropic(provider.BaseURL) {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}

	return resp, nil
}

// CallStream makes a streaming request (sets stream=true in body).
func (c *Client) CallStream(provider types.ProviderConfig, body map[string]interface{}) (*http.Response, error) {
	body["stream"] = true
	return c.Call(provider, body)
}
