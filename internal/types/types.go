package types

type ProviderConfig struct {
	Name              string  `json:"name"`
	BaseURL           string  `json:"base_url"`
	Model             string  `json:"model"`
	Format            string  `json:"format"`
	Timeout           int     `json:"timeout"`
	APIKey            string  `json:"api_key,omitempty"`
	MaskedKey         string  `json:"masked_key,omitempty"`
	WeeklyTokenLimit  *uint64 `json:"weekly_token_limit,omitempty"`
	WeeklyReqLimit    *uint64 `json:"weekly_request_limit,omitempty"`
	ContextLength     *int    `json:"context_length,omitempty"`
	MaxOutputTokens   *int    `json:"max_output_tokens,omitempty"`
}

type PlanConfig struct {
	Providers []ProviderConfig `json:"providers"`
}

type PlansConfig struct {
	Plans map[string]PlanConfig `yaml:"plans"`
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

type StatRecord struct {
	Plan           string `json:"plan"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	KeyMask        string `json:"key_mask,omitempty"`
	RequestTokens  int    `json:"request_tokens"`
	ResponseTokens int    `json:"response_tokens"`
	TotalTokens    int    `json:"total_tokens"`
	Status         string `json:"status"`
	LatencyMs      int64  `json:"latency_ms"`
	IsStreaming    bool   `json:"is_streaming"`
	TargetProvider string `json:"target_provider,omitempty"`
}
