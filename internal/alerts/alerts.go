package alerts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"time"
)

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
	go func() {
		resp, err := http.Post(webhookURL, "application/json", bytes.NewReader(body))
		if err != nil {
			return
		}
		defer resp.Body.Close()
	}()
}

// SendWebhookExpiredAlert POSTs a JSON payload when a key expires.
func SendWebhookExpiredAlert(webhookURL, keyName string) {
	if webhookURL == "" {
		return
	}
	body, _ := json.Marshal(map[string]interface{}{
		"event":     "key_expired",
		"key_name":  keyName,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	go func() {
		resp, err := http.Post(webhookURL, "application/json", bytes.NewReader(body))
		if err != nil {
			return
		}
		defer resp.Body.Close()
	}()
}
