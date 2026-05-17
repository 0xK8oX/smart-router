package alerts

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"smart-router/internal/db"
	"smart-router/internal/health"
	"smart-router/internal/types"
)

// Bot polls Telegram for commands and responds with router stats/health.
type Bot struct {
	token   string
	db      *db.DB
	health  *health.HealthTracker
	offset  int64
	client  *http.Client
	stop    chan struct{}
}

// StartBot launches a background goroutine that polls Telegram for commands.
// It returns immediately; the bot runs until the process exits.
func StartBot(database *db.DB, ht *health.HealthTracker) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return
	}

	b := &Bot{
		token:  token,
		db:     database,
		health: ht,
		client: &http.Client{Timeout: 30 * time.Second},
		stop:   make(chan struct{}),
	}

	go b.pollLoop()
	log.Println("telegram bot polling started")
}

type telegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

func (b *Bot) pollLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.pollOnce()
		case <-b.stop:
			return
		}
	}
}

func (b *Bot) pollOnce() {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&limit=10", b.token, b.offset+1)
	resp, err := b.client.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool             `json:"ok"`
		Result []telegramUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return
	}
	if !result.OK {
		return
	}

	for _, u := range result.Result {
		if u.UpdateID > b.offset {
			b.offset = u.UpdateID
		}
		if u.Message.Text != "" {
			go b.handleCommand(u.Message.Chat.ID, u.Message.Text)
		}
	}
}

func (b *Bot) handleCommand(chatID int64, text string) {
	reply := b.buildReply(text)
	b.sendMessage(chatID, reply)
}

func (b *Bot) buildReply(text string) string {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return ""
	}

	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	switch cmd {
	case "/plan":
		return b.cmdPlan(args)
	case "/health":
		return b.cmdHealth(args)
	case "/stats":
		return b.cmdStats(args)
	case "/usage":
		return b.cmdUsage(args)
	case "/plans":
		return b.cmdPlans()
	case "/status":
		return b.cmdStatus()
	case "/top":
		return b.cmdTop()
	case "/failures":
		return b.cmdFailures()
	case "/help", "/start":
		return b.cmdHelp()
	default:
		return "Unknown command. Type /help for available commands."
	}
}

func (b *Bot) cmdPlan(args []string) string {
	if len(args) < 1 {
		return "Usage: /plan <slug>"
	}
	slug := args[0]

	plan, err := b.db.GetPlan(slug)
	if err != nil {
		return fmt.Sprintf("Plan *%s* not found.", slug)
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("*Plan: %s*", slug))
	lines = append(lines, fmt.Sprintf("Providers: %d", len(plan.Providers)))

	for _, p := range plan.Providers {
		mask := p.MaskedKey
		if mask == "" {
			mask = p.Name
		}
		h, _ := b.health.GetHealth(p.Name)
		status := h.Status
		if status == "" {
			status = "unknown"
		}
		statusEmoji := "🟢"
		if status == "unhealthy" {
			statusEmoji = "🔴"
		}

		var limits []string
		if p.WeeklyTokenLimit != nil {
			limits = append(limits, fmt.Sprintf("token_limit=%d", *p.WeeklyTokenLimit))
		}
		if p.WeeklyReqLimit != nil {
			limits = append(limits, fmt.Sprintf("req_limit=%d", *p.WeeklyReqLimit))
		}
		limitStr := ""
		if len(limits) > 0 {
			limitStr = " (" + strings.Join(limits, ", ") + ")"
		}

		lines = append(lines, fmt.Sprintf("  %s *%s* — model=%s format=%s%s", statusEmoji, p.Name, p.Model, p.Format, limitStr))

		if h.ConsecutiveFailures > 0 {
			lines = append(lines, fmt.Sprintf("    failures=%d reason=%s", h.ConsecutiveFailures, h.LastFailureReason))
		}
		if mask != p.Name {
			usage, _ := b.db.GetWeeklyUsage(mask)
			if usage != nil {
				lines = append(lines, fmt.Sprintf("    weekly: %d req, %d tok", usage.RequestCount, usage.RequestTokens+usage.ResponseTokens))
			}
		}
	}

	return strings.Join(lines, "\n")
}

func (b *Bot) cmdHealth(args []string) string {
	if len(args) > 0 {
		name := args[0]
		h, err := b.health.GetHealth(name)
		if err != nil {
			return fmt.Sprintf("Error fetching health for *%s*.", name)
		}
		return formatHealth(name, h)
	}

	plans, err := b.db.ListPlans()
	if err != nil {
		return "Error listing plans."
	}

	var lines []string
	lines = append(lines, "*Provider Health*")
	seen := make(map[string]bool)

	for _, plan := range plans {
		for _, p := range plan.Providers {
			if seen[p.Name] {
				continue
			}
			seen[p.Name] = true
			h, _ := b.health.GetHealth(p.Name)
			lines = append(lines, formatHealthLine(p.Name, h))
		}
	}

	return strings.Join(lines, "\n")
}

func (b *Bot) cmdStats(args []string) string {
	plan := ""
	provider := ""
	limit := 10

	if len(args) > 0 {
		plan = args[0]
	}
	if len(args) > 1 {
		provider = args[1]
	}
	if len(args) > 2 {
		if n, err := strconv.Atoi(args[2]); err == nil && n > 0 {
			limit = n
			if limit > 50 {
				limit = 50
			}
		}
	}

	stats, err := b.db.GetStats(plan, provider, limit)
	if err != nil {
		return "Error fetching stats."
	}
	if len(stats) == 0 {
		return "No stats found."
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("*Recent Stats* (showing %d)", len(stats)))
	for _, s := range stats {
		emoji := "🟢"
		if s.Status != "success" {
			emoji = "🔴"
		}
		lines = append(lines, fmt.Sprintf("%s `%s/%s` %s %dms %d tok", emoji, s.Plan, s.Provider, s.Status, s.LatencyMs, s.TotalTokens))
	}

	return strings.Join(lines, "\n")
}

func (b *Bot) cmdUsage(args []string) string {
	if len(args) < 1 {
		return "Usage: /usage <key_mask_or_provider>"
	}
	keyMask := args[0]

	usage, err := b.db.GetWeeklyUsage(keyMask)
	if err != nil {
		return fmt.Sprintf("Error fetching usage for *%s*.", keyMask)
	}

	return fmt.Sprintf("*Weekly Usage: %s*\nRequests: %d\nTokens: %d (%d in / %d out)",
		keyMask, usage.RequestCount, usage.RequestTokens+usage.ResponseTokens, usage.RequestTokens, usage.ResponseTokens)
}

func (b *Bot) cmdPlans() string {
	plans, err := b.db.ListPlans()
	if err != nil {
		return "Error listing plans."
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("*Plans (%d)*", len(plans)))
	for slug, plan := range plans {
		healthy := 0
		unhealthy := 0
		for _, p := range plan.Providers {
			h, _ := b.health.GetHealth(p.Name)
			if h.Status == "unhealthy" {
				unhealthy++
			} else {
				healthy++
			}
		}
		lines = append(lines, fmt.Sprintf("  `%s` — %d providers (%d🟢 %d🔴)", slug, len(plan.Providers), healthy, unhealthy))
	}

	return strings.Join(lines, "\n")
}

func (b *Bot) cmdStatus() string {
	plans, err := b.db.ListPlans()
	if err != nil {
		return "Error fetching status."
	}

	totalProviders := 0
	healthyCount := 0
	unhealthyCount := 0
	seen := make(map[string]bool)

	for _, plan := range plans {
		for _, p := range plan.Providers {
			if seen[p.Name] {
				continue
			}
			seen[p.Name] = true
			totalProviders++
			h, _ := b.health.GetHealth(p.Name)
			if h.Status == "unhealthy" {
				unhealthyCount++
			} else {
				healthyCount++
			}
		}
	}

	var lines []string
	lines = append(lines, "*Smart Router Status*")
	lines = append(lines, fmt.Sprintf("Plans: %d", len(plans)))
	lines = append(lines, fmt.Sprintf("Providers: %d total (%d🟢 %d🔴)", totalProviders, healthyCount, unhealthyCount))

	if unhealthyCount > 0 {
		lines = append(lines, "\n*Unhealthy Providers:*")
		seen = make(map[string]bool)
		for _, plan := range plans {
			for _, p := range plan.Providers {
				if seen[p.Name] {
					continue
				}
				seen[p.Name] = true
				h, _ := b.health.GetHealth(p.Name)
				if h.Status == "unhealthy" {
					lines = append(lines, fmt.Sprintf("  🔴 *%s* — %d failures, reason: %s", p.Name, h.ConsecutiveFailures, h.LastFailureReason))
				}
			}
		}
	}

	return strings.Join(lines, "\n")
}

func (b *Bot) cmdTop() string {
	// Get last 24h of stats
	stats, err := b.db.GetStats("", "", 500)
	if err != nil {
		return "Error fetching stats."
	}

	cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()
	providerStats := make(map[string]struct {
		requests int64
		tokens   int64
		success  int64
		failure  int64
	})

	for _, s := range stats {
		// Stats don't have timestamps in the struct, so we can't filter by time
		// without modifying the DB layer. For now, use all returned stats.
		_ = cutoff
		ps := providerStats[s.Provider]
		ps.requests++
		ps.tokens += int64(s.TotalTokens)
		if s.Status == "success" {
			ps.success++
		} else {
			ps.failure++
		}
		providerStats[s.Provider] = ps
	}

	if len(providerStats) == 0 {
		return "No stats recorded yet."
	}

	type item struct {
		name     string
		requests int64
		tokens   int64
		success  int64
		failure  int64
	}
	var items []item
	for name, ps := range providerStats {
		items = append(items, item{name, ps.requests, ps.tokens, ps.success, ps.failure})
	}

	// Sort by requests descending
	for i := 0; i < len(items)-1; i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].requests > items[i].requests {
				items[i], items[j] = items[j], items[i]
			}
		}
	}

	var lines []string
	lines = append(lines, "*Top Providers (last 500 records)*")
	for i, it := range items {
		if i >= 10 {
			break
		}
		pct := int64(0)
		if it.requests > 0 {
			pct = it.success * 100 / it.requests
		}
		lines = append(lines, fmt.Sprintf("%d. *%s* — %d req, %d tok, %d%% success", i+1, it.name, it.requests, it.tokens, pct))
	}

	return strings.Join(lines, "\n")
}

func (b *Bot) cmdFailures() string {
	stats, err := b.db.GetStats("", "", 20)
	if err != nil {
		return "Error fetching stats."
	}

	var failures []types.StatRecord
	for _, s := range stats {
		if s.Status == "failure" || s.Status == "error" {
			failures = append(failures, s)
		}
	}

	if len(failures) == 0 {
		return "No recent failures."
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("*Recent Failures (%d shown)*", len(failures)))
	for _, s := range failures {
		lines = append(lines, fmt.Sprintf("🔴 `%s/%s` %s %dms", s.Plan, s.Provider, s.Status, s.LatencyMs))
	}

	return strings.Join(lines, "\n")
}

func (b *Bot) cmdHelp() string {
	return `*Smart Router Bot Commands*

/plan <slug> — Show plan config and provider health
/health [provider] — Show health for all or specific provider
/stats [plan] [provider] [limit] — Recent request stats
/usage <key_mask> — Weekly token/request usage
/plans — List all plans with provider counts
/status — Overall system status
/top — Top providers by usage
/failures — Recent failed requests
/help — Show this message`
}

func formatHealth(name string, h types.ProviderHealth) string {
	status := h.Status
	if status == "" {
		status = "unknown"
	}
	emoji := "🟢"
	if status == "unhealthy" {
		emoji = "🔴"
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("*%s*: %s %s", name, emoji, status))
	if h.ConsecutiveFailures > 0 {
		lines = append(lines, fmt.Sprintf("Consecutive failures: %d", h.ConsecutiveFailures))
		lines = append(lines, fmt.Sprintf("Last failure reason: %s", h.LastFailureReason))
	}
	if h.TotalRequests > 0 {
		lines = append(lines, fmt.Sprintf("Total requests: %d", h.TotalRequests))
		if h.SuccessCount > 0 {
			lines = append(lines, fmt.Sprintf("Success rate: %d%%", h.SuccessCount*100/h.TotalRequests))
		}
	}
	if h.CooldownUntil > 0 {
		lines = append(lines, fmt.Sprintf("Cooldown until: %s", time.Unix(h.CooldownUntil, 0).Format(time.RFC3339)))
	}
	return strings.Join(lines, "\n")
}

func formatHealthLine(name string, h types.ProviderHealth) string {
	status := h.Status
	if status == "" {
		status = "unknown"
	}
	emoji := "🟢"
	if status == "unhealthy" {
		emoji = "🔴"
	}
	extra := ""
	if h.ConsecutiveFailures > 0 {
		extra = fmt.Sprintf(" (%d failures, %s)", h.ConsecutiveFailures, h.LastFailureReason)
	}
	return fmt.Sprintf("  %s *%s* — %s%s", emoji, name, status, extra)
}

func (b *Bot) sendMessage(chatID int64, text string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", b.token)
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	})

	resp, err := b.client.Post(url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		log.Printf("telegram send message error: %v", err)
		return
	}
	defer resp.Body.Close()
}
