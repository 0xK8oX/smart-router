package alerts

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	// Save and restore env vars
	oldToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	oldChatID := os.Getenv("TELEGRAM_CHAT_ID")
	oldDiscord := os.Getenv("DISCORD_WEBHOOK_URL")
	defer func() {
		os.Setenv("TELEGRAM_BOT_TOKEN", oldToken)
		os.Setenv("TELEGRAM_CHAT_ID", oldChatID)
		os.Setenv("DISCORD_WEBHOOK_URL", oldDiscord)
	}()

	os.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	os.Setenv("TELEGRAM_CHAT_ID", "12345")
	os.Setenv("DISCORD_WEBHOOK_URL", "https://discord.com/webhook")

	cfg := LoadConfig()
	if cfg.TelegramBotToken != "test-token" {
		t.Errorf("expected token=test-token, got %q", cfg.TelegramBotToken)
	}
	if cfg.TelegramChatID != "12345" {
		t.Errorf("expected chatID=12345, got %q", cfg.TelegramChatID)
	}
	if cfg.DiscordWebhook != "https://discord.com/webhook" {
		t.Errorf("expected discord webhook, got %q", cfg.DiscordWebhook)
	}
}

func TestConfigIsConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"all", Config{TelegramBotToken: "t", TelegramChatID: "c", DiscordWebhook: "d"}, true},
		{"telegram only", Config{TelegramBotToken: "t", TelegramChatID: "c"}, true},
		{"discord only", Config{DiscordWebhook: "d"}, true},
		{"empty", Config{}, false},
		{"token no chat", Config{TelegramBotToken: "t"}, false},
		{"chat no token", Config{TelegramChatID: "c"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsConfigured(); got != tt.want {
				t.Errorf("IsConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSendWebhookQuotaAlert(t *testing.T) {
	done := make(chan map[string]interface{}, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		json.Unmarshal(body, &payload)
		done <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	SendWebhookQuotaAlert(ts.URL, "test-key", 85.5, 1000, 500, 42)

	var payload map[string]interface{}
	select {
	case payload = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected webhook to be called")
	}

	if payload["event"] != "quota_alert" {
		t.Errorf("expected event=quota_alert, got %v", payload["event"])
	}
	if payload["key_name"] != "test-key" {
		t.Errorf("expected key_name=test-key, got %v", payload["key_name"])
	}
	if payload["usage_percent"] != 85.5 {
		t.Errorf("expected usage_percent=85.5, got %v", payload["usage_percent"])
	}

	// Empty URL should not panic
	SendWebhookQuotaAlert("", "key", 0, 0, 0, 0)
}

func TestSendWebhookExpiredAlert(t *testing.T) {
	done := make(chan map[string]interface{}, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		json.Unmarshal(body, &payload)
		done <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	SendWebhookExpiredAlert(ts.URL, "expired-key")

	var payload map[string]interface{}
	select {
	case payload = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected webhook to be called")
	}

	if payload["event"] != "key_expired" {
		t.Errorf("expected event=key_expired, got %v", payload["event"])
	}
	if payload["key_name"] != "expired-key" {
		t.Errorf("expected key_name=expired-key, got %v", payload["key_name"])
	}

	// Empty URL should not panic
	SendWebhookExpiredAlert("", "key")
}

