package alerts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"
)

var webhookClient = &http.Client{
	Timeout: 10 * time.Second,
}

var (
	expiredAlertMu    sync.Mutex
	expiredAlertCache = make(map[string]time.Time) // key -> last alert time
	expiredAlertTTL   = 1 * time.Hour
)

func sendWebhookWithRetry(webhookURL string, body []byte) {
	const maxRetries = 3
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i) * time.Second)
		}
		resp, err := webhookClient.Post(webhookURL, "application/json", bytes.NewReader(body))
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode < 500 {
			return
		}
	}
}

// Config holds alert notification configuration from environment variables.
type Config struct {
	TelegramBotToken string
	TelegramChatID   string
	DiscordWebhook   string
}

// LoadConfig reads alert configuration from environment variables.
func LoadConfig() Config {
	return Config{
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   os.Getenv("TELEGRAM_CHAT_ID"),
		DiscordWebhook:   os.Getenv("DISCORD_WEBHOOK_URL"),
	}
}

// IsConfigured returns true if at least one alert channel is configured.
func (c Config) IsConfigured() bool {
	return c.TelegramBotToken != "" && c.TelegramChatID != "" || c.DiscordWebhook != ""
}

// ProviderError describes a single provider failure.
type ProviderError struct {
	Name   string
	Status int
	Msg    string
}

// SendWebhookQuotaAlert POSTs a JSON payload to the given webhook URL when a key hits its quota threshold.
func SendWebhookQuotaAlert(webhookURL, keyName string, usagePercent float64, reqTokens, respTokens, reqCount int64) {
	if webhookURL == "" {
		return
	}
	body, _ := json.Marshal(map[string]interface{}{
		"event":          "quota_alert",
		"key_name":       keyName,
		"usage_percent":  usagePercent,
		"request_tokens": reqTokens,
		"response_tokens": respTokens,
		"request_count":  reqCount,
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
	})
	go sendWebhookWithRetry(webhookURL, body)
}

// SendWebhookExpiredAlert POSTs a JSON payload when a key expires.
// Alerts are deduplicated per key to at most once per hour.
func SendWebhookExpiredAlert(webhookURL, keyName string) {
	if webhookURL == "" {
		return
	}

	now := time.Now()
	expiredAlertMu.Lock()
	// Evict stale entries to prevent unbounded growth.
	for k, t := range expiredAlertCache {
		if now.Sub(t) >= expiredAlertTTL {
			delete(expiredAlertCache, k)
		}
	}
	lastAlert, ok := expiredAlertCache[keyName]
	if ok && now.Sub(lastAlert) < expiredAlertTTL {
		expiredAlertMu.Unlock()
		return
	}
	expiredAlertCache[keyName] = now
	expiredAlertMu.Unlock()

	body, _ := json.Marshal(map[string]interface{}{
		"event":     "key_expired",
		"key_name":  keyName,
		"timestamp": now.UTC().Format(time.RFC3339),
	})
	go sendWebhookWithRetry(webhookURL, body)
}
