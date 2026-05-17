package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
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

// SendOutageAlert sends notifications when all providers in a plan fail.
// Errors are logged but not returned to avoid disrupting the request flow.
func SendOutageAlert(plan string, errors []ProviderError) {
	cfg := LoadConfig()
	if !cfg.IsConfigured() {
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var lines []string
	for _, e := range errors {
		msg := e.Msg
		if len(msg) > 60 {
			msg = msg[:60] + "..."
		}
		statusStr := "error"
		if e.Status > 0 {
			statusStr = fmt.Sprintf("HTTP %d", e.Status)
		}
		lines = append(lines, fmt.Sprintf("  • %s: %s — %s", e.Name, statusStr, msg))
	}

	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		text := fmt.Sprintf(
			"⚠️ *SMART ROUTER OUTAGE*\n*Plan:* `%s`\n*Time:* %s\n*All providers failed:*\n%s\n\nCheck status: `/v1/status`",
			plan, now, joinLines(lines),
		)
		go sendTelegram(cfg.TelegramBotToken, cfg.TelegramChatID, text)
	}

	if cfg.DiscordWebhook != "" {
		description := fmt.Sprintf("**Time:** %s\n**All providers failed:**\n%s", now, joinLines(lines))
		go sendDiscord(cfg.DiscordWebhook, plan, description)
	}
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n") + "\n"
}

func sendTelegram(botToken, chatID, text string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	})

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

func sendDiscord(webhook, plan, description string) {
	body, _ := json.Marshal(map[string]interface{}{
		"embeds": []map[string]interface{}{
			{
				"title":       "⚠️ Smart Router Outage",
				"description": description,
				"color":       0xFF4444,
				"fields": []map[string]interface{}{
					{"name": "Plan", "value": plan, "inline": true},
				},
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			},
		},
	})

	resp, err := http.Post(webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	defer resp.Body.Close()
}
