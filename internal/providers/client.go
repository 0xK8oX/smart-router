package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
				ForceAttemptHTTP2:     true,
			},
		},
	}
}

func buildEndpoint(baseURL string, format string) string {
	trimmed := strings.TrimSuffix(baseURL, "/")
	// If the URL already ends in a known API path, treat it as fully specified
	// and return it verbatim. This lets providers with non-OpenAI path
	// conventions (e.g. bigmodel's /api/paas/v4/chat/completions) declare their
	// exact endpoint URL without needing buildEndpoint to guess.
	for _, suffix := range []string{"/chat/completions", "/messages", "/responses", "/completions"} {
		if strings.HasSuffix(trimmed, suffix) {
			return trimmed
		}
	}
	base := strings.TrimSuffix(trimmed, "/v1")
	switch format {
	case "anthropic":
		return base + "/v1/messages"
	case "responses":
		return base + "/v1/responses"
	default:
		return base + "/v1/chat/completions"
	}
}

// buildModelsEndpoint returns the upstream /v1/models URL for a provider.
func buildModelsEndpoint(baseURL string) string {
	return strings.TrimSuffix(baseURL, "/") + "/v1/models"
}

func isKimiCodingEndpoint(baseURL string) bool {
	return strings.Contains(baseURL, "api.kimi.com") && strings.Contains(baseURL, "/coding")
}

func isNativeAnthropic(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return u.Hostname() == "api.anthropic.com"
}

func (c *Client) doRequest(ctx context.Context, provider types.ProviderConfig, body map[string]interface{}, headers http.Header) (*http.Response, error) {
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

	// Strip hop-by-hop and client-specific headers that must not reach upstream.
	for _, h := range []string{
		"Host", "Cookie", "Referer", "Connection", "Keep-Alive",
		"Upgrade", "Accept-Encoding", "Content-Length", "Transfer-Encoding",
		"Authorization", "x-api-key", "X-Admin-Key",
	} {
		req.Header.Del(h)
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
func (c *Client) Call(ctx context.Context, provider types.ProviderConfig, body map[string]interface{}, headers http.Header) (*http.Response, error) {
	timeout := time.Duration(provider.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	resp, err := c.doRequest(ctx, provider, body, headers)
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
func (c *Client) CallStream(ctx context.Context, provider types.ProviderConfig, body map[string]interface{}, headers http.Header) (*http.Response, error) {
	streamBody := make(map[string]interface{}, len(body)+1)
	for k, v := range body {
		streamBody[k] = v
	}
	streamBody["stream"] = true
	return c.doRequest(ctx, provider, streamBody, headers)
}

// FetchModels calls a provider's /v1/models endpoint and returns the raw JSON
// bytes. It uses a short timeout and never retries.
func (c *Client) FetchModels(ctx context.Context, provider types.ProviderConfig) ([]byte, error) {
	timeout := 5 * time.Second
	if provider.Timeout > 0 {
		timeout = time.Duration(provider.Timeout) * time.Second
		if timeout > 5*time.Second {
			timeout = 5 * time.Second
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	endpoint := buildModelsEndpoint(provider.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}
	if isNativeAnthropic(provider.BaseURL) {
		req.Header.Set("x-api-key", provider.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if isKimiCodingEndpoint(provider.BaseURL) {
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
		req.Header.Set("User-Agent", "claude-code/0.1.0")
	} else {
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
