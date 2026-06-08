package types

type ProviderConfig struct {
	Name              string  `json:"name" yaml:"name"`
	BaseURL           string  `json:"base_url" yaml:"base_url"`
	Model             string  `json:"model" yaml:"model"`
	Format            string  `json:"format" yaml:"format"`
	Timeout           int     `json:"timeout" yaml:"timeout"`
	APIKey            string  `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	MaskedKey         string  `json:"masked_key,omitempty" yaml:"masked_key,omitempty"`
	WeeklyTokenLimit  *uint64 `json:"weekly_token_limit,omitempty" yaml:"weekly_token_limit,omitempty"`
	WeeklyReqLimit    *uint64 `json:"weekly_request_limit,omitempty" yaml:"weekly_request_limit,omitempty"`
	ContextLength     *int    `json:"context_length,omitempty" yaml:"context_length,omitempty"`
	MaxOutputTokens   *int    `json:"max_output_tokens,omitempty" yaml:"max_output_tokens,omitempty"`
	MaxConcurrency    *int    `json:"max_concurrency,omitempty" yaml:"max_concurrency,omitempty"`
	Weight            int     `json:"weight,omitempty" yaml:"weight,omitempty"`
}

type PlanConfig struct {
	Strategy  string           `json:"strategy,omitempty" yaml:"strategy,omitempty"`
	Providers []ProviderConfig `json:"providers" yaml:"providers"`
}

type ProviderHealth struct {
	Status              string `json:"status"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	LastFailureAt       int64  `json:"lastFailureAt"`
	CooldownUntil       int64  `json:"cooldownUntil"`
	LastFailureReason   string `json:"lastFailureReason"`
	LastSuccessAt       int64  `json:"lastSuccessAt"`
	TotalRequests       int64  `json:"totalRequests"`
	SuccessCount        int64  `json:"successCount"`
	LastActivityAt      int64  `json:"lastActivityAt"`
}

type APIKey struct {
	Key                 string   `json:"key"`
	Name                string   `json:"name"`
	Plans               []string `json:"plans"`
	Models              []string `json:"models"`
	AllowedIPs          []string `json:"allowed_ips"`
	RateLimitRPM        int      `json:"rate_limit_rpm"`
	RateLimitRPD        int      `json:"rate_limit_rpd"`
	MonthlyTokenLimit   int      `json:"monthly_token_limit"`
	MonthlyRequestLimit int      `json:"monthly_request_limit"`
	ExpiresAt           *int64   `json:"expires_at,omitempty"`
	Disabled            bool     `json:"disabled"`
	CreatedAt           int64    `json:"created_at"`
	LastUsedAt          *int64   `json:"last_used_at,omitempty"`
	WebhookURL          string   `json:"webhook_url,omitempty"`
	GroupID             *int64   `json:"group_id,omitempty"`
}

type KeyGroup struct {
	ID                  int64   `json:"id"`
	Name                string  `json:"name"`
	MonthlyTokenLimit   int     `json:"monthly_token_limit"`
	MonthlyRequestLimit int     `json:"monthly_request_limit"`
	MonthlyBudgetLimit  float64 `json:"monthly_budget_limit"`
	WebhookURL          string  `json:"webhook_url,omitempty"`
}

type StatRecord struct {
	Plan           string `json:"plan"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	KeyMask        string `json:"key_mask,omitempty"`
	ClientKey      string `json:"client_key,omitempty"`
	Source         string `json:"source,omitempty"`
	RequestTokens  int    `json:"request_tokens"`
	ResponseTokens int    `json:"response_tokens"`
	TotalTokens    int    `json:"total_tokens"`
	Status         string `json:"status"`
	StatusCode     int    `json:"status_code"`
	ErrorReason    string `json:"error_reason,omitempty"`
	LatencyMs      int64  `json:"latency_ms"`
	IsStreaming    bool   `json:"is_streaming"`
	TargetProvider string `json:"target_provider,omitempty"`
}

// MaskAPIKey returns a masked version of an API key for display/logging.
// Only the last 4 characters are preserved; everything else is masked.
func MaskAPIKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}
