package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"smart-router/internal/types"
)

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

// Call makes a non-streaming request to the provider.
func (c *Client) Call(provider types.ProviderConfig, body map[string]interface{}) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(provider.Timeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.BaseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	if provider.Format == "anthropic" {
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
