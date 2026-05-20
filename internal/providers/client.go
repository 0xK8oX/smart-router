package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"smart-router/internal/types"
)

// Client is an HTTP client for calling upstream LLM providers.
type Client struct {
	httpClient *http.Client
}

// NewClient creates a new provider HTTP client with connection timeouts.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
				IdleConnTimeout:       90 * time.Second,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   10,
			},
		},
	}
}

func buildEndpoint(baseURL string, format string) string {
	base := strings.TrimSuffix(baseURL, "/v1")
	if format == "anthropic" {
		return base + "/v1/messages"
	}
	return base + "/v1/chat/completions"
}

func isKimiCodingEndpoint(baseURL string) bool {
	return strings.Contains(baseURL, "api.kimi.com") && strings.Contains(baseURL, "/coding")
}

func isNativeAnthropic(baseURL string) bool {
	return strings.Contains(baseURL, "api.anthropic.com")
}

func (c *Client) doRequest(provider types.ProviderConfig, body map[string]interface{}, ctx context.Context, headers http.Header) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}

	endpoint := buildEndpoint(provider.BaseURL, provider.Format)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Forward original headers when in passthrough mode (clientFormat == provider.Format).
	if headers != nil {
		for k, vv := range headers {
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}
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

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelBody) Close() error {
	c.cancel()
	return c.ReadCloser.Close()
}

// Call makes a non-streaming request to the provider.
// headers is forwarded as-is when clientFormat == provider.Format (passthrough mode).
func (c *Client) Call(provider types.ProviderConfig, body map[string]interface{}, headers http.Header) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(provider.Timeout)*time.Second)
	resp, err := c.doRequest(provider, body, ctx, headers)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// CallStream makes a streaming request (sets stream=true in body).
// No hard timeout — streams can run indefinitely. The router's handler
// timeout and upstream provider behavior are the natural boundaries.
// headers is forwarded as-is when clientFormat == provider.Format (passthrough mode).
func (c *Client) CallStream(provider types.ProviderConfig, body map[string]interface{}, headers http.Header) (*http.Response, error) {
	body["stream"] = true
	return c.doRequest(provider, body, context.Background(), headers)
}
