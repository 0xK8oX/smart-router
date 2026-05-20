package alerts

import (
	"os"
	"strings"
	"testing"
	"time"

	"smart-router/internal/db"
	"smart-router/internal/health"
	"smart-router/internal/types"
)

func setupTestBot(t *testing.T) (*Bot, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "telegram-test-*")
	if err != nil {
		t.Fatal(err)
	}

	sqlitePath := dir + "/test.db"
	database, err := db.Open(sqlitePath)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("open db: %v", err)
	}

	ht, err := health.New(dir + "/health")
	if err != nil {
		database.Close()
		os.RemoveAll(dir)
		t.Fatalf("open health: %v", err)
	}

	b := &Bot{
		token:  "dummy",
		db:     database,
		health: ht,
	}

	cleanup := func() {
		ht.Close()
		database.Close()
		os.RemoveAll(dir)
	}

	return b, cleanup
}

func TestBotCommandParsing(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	// Test help command
	reply := b.buildReply("/help")
	if reply == "" {
		t.Error("expected help reply")
	}

	// Test unknown command
	reply = b.buildReply("/unknown")
	if reply == "" {
		t.Error("expected unknown command reply")
	}
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0K"},
		{1500, "1.5K"},
		{999999, "1000.0K"},
		{1000000, "1.0M"},
		{2500000, "2.5M"},
		{999999999, "1000.0M"},
		{1500000000, "1.5B"},
	}
	for _, tt := range tests {
		got := formatNumber(tt.input)
		if got != tt.want {
			t.Errorf("formatNumber(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseTimeWindow(t *testing.T) {
	tests := []struct {
		input     string
		wantDur   time.Duration
		wantLabel string
	}{
		{"1h", time.Hour, "1h"},
		{"24h", 24 * time.Hour, "24h"},
		{"7d", 7 * 24 * time.Hour, "1 week"},
		{"30d", 30 * 24 * time.Hour, "30 days"},
		{"", 30 * 24 * time.Hour, "30 days"}, // default
		{"1d", 24 * time.Hour, "1 day"},
		{"5h", 5 * time.Hour, "5 hours"},
		{"1m", 30 * 24 * time.Hour, "1 month"},
		{"1w", 7 * 24 * time.Hour, "1 week"},
		{"invalid", 30 * 24 * time.Hour, "30 days"}, // invalid defaults to 30d
		{"90s", 90 * time.Second, "90s"}, // Go duration fallback
	}
	for _, tt := range tests {
		gotDur, gotLabel := parseTimeWindow(tt.input)
		if gotDur != tt.wantDur {
			t.Errorf("parseTimeWindow(%q) dur = %v, want %v", tt.input, gotDur, tt.wantDur)
		}
		if gotLabel != tt.wantLabel {
			t.Errorf("parseTimeWindow(%q) label = %q, want %q", tt.input, gotLabel, tt.wantLabel)
		}
	}
}

func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"sr-abc123def456", "sr-abc****f456"},
		{"short", "****"},
		{"", "****"},
		{"ab", "****"},
		{"sr-", "****"},
	}
	for _, tt := range tests {
		got := maskAPIKey(tt.input)
		if got != tt.want {
			t.Errorf("maskAPIKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatHealth(t *testing.T) {
	now := time.Now()
	h := types.ProviderHealth{
		Status:              "healthy",
		ConsecutiveFailures: 0,
		LastActivityAt:      now.Unix(),
	}
	got := formatHealth("anthropic", h)
	if got == "" {
		t.Error("expected non-empty health string")
	}
	if !contains(got, "healthy") {
		t.Errorf("expected health string to contain 'healthy', got %q", got)
	}

	// Unhealthy
	h.Status = "unhealthy"
	h.ConsecutiveFailures = 5
	got = formatHealth("anthropic", h)
	if !contains(got, "unhealthy") {
		t.Errorf("expected health string to contain 'unhealthy', got %q", got)
	}

	// Healthy provider with last activity
	h2 := types.ProviderHealth{
		Status:         "healthy",
		LastActivityAt: now.Unix(),
	}
	got2 := formatHealth("openai", h2)
	if !contains(got2, "healthy") {
		t.Errorf("expected healthy status, got %q", got2)
	}

	// Unhealthy provider with cooldown
	h3 := types.ProviderHealth{
		Status:        "unhealthy",
		CooldownUntil: now.Add(time.Hour).Unix(),
	}
	got3 := formatHealth("gemini", h3)
	if !contains(got3, "unhealthy") {
		t.Errorf("expected unhealthy status, got %q", got3)
	}
	if !contains(got3, "Cooldown") {
		t.Errorf("expected cooldown info, got %q", got3)
	}

	// Provider with zero last activity
	h4 := types.ProviderHealth{
		Status:         "",
		LastActivityAt: 0,
	}
	got4 := formatHealth("test", h4)
	if !contains(got4, "unknown") {
		t.Errorf("expected unknown status for empty health, got %q", got4)
	}

	// Consecutive failures with reason
	h5 := types.ProviderHealth{
		Status:              "unhealthy",
		ConsecutiveFailures: 3,
		LastFailureReason:   "connection refused",
	}
	got5 := formatHealth("test", h5)
	if !contains(got5, "Consecutive failures: 3") {
		t.Errorf("expected consecutive failures info, got %q", got5)
	}
	if !contains(got5, "connection refused") {
		t.Errorf("expected last failure reason, got %q", got5)
	}

	// With total requests and success count
	h6 := types.ProviderHealth{
		Status:         "healthy",
		TotalRequests:  100,
		SuccessCount:   95,
		LastActivityAt: now.Unix(),
	}
	got6 := formatHealth("test", h6)
	if !contains(got6, "Total requests: 100") {
		t.Errorf("expected total requests, got %q", got6)
	}
	if !contains(got6, "Success rate: 95%") {
		t.Errorf("expected success rate, got %q", got6)
	}
}

func TestFormatHealthLine(t *testing.T) {
	now := time.Now()
	h := types.ProviderHealth{
		Status:              "healthy",
		ConsecutiveFailures: 0,
		LastActivityAt:      now.Unix(),
	}
	got := formatHealthLine("anthropic", h)
	if got == "" {
		t.Error("expected non-empty health line")
	}

	// Unhealthy with cooldown
	h.Status = "unhealthy"
	h.CooldownUntil = now.Add(time.Hour).Unix()
	got = formatHealthLine("anthropic", h)
	if !contains(got, "unhealthy") {
		t.Errorf("expected line to contain 'unhealthy', got %q", got)
	}
}

func TestBuildReply(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	// Help
	reply := b.buildReply("/help")
	if !contains(reply, "Smart Router") && !contains(reply, "Commands") {
		t.Errorf("expected help text, got %q", reply)
	}

	// Empty
	reply = b.buildReply("")
	if reply != "" {
		t.Errorf("expected empty reply for empty input, got %q", reply)
	}

	// Usage command
	_ = b.db.SavePlan("default", types.PlanConfig{})
	reply = b.buildReply("/usage")
	if reply == "" {
		t.Error("expected non-empty reply for /usage")
	}

	// Top command
	reply = b.buildReply("/top")
	if reply == "" {
		t.Error("expected non-empty reply for /top")
	}

	// Failures command
	reply = b.buildReply("/failures")
	if reply == "" {
		t.Error("expected non-empty reply for /failures")
	}

	// Keyusage command
	reply = b.buildReply("/keyusage testkey")
	if reply == "" {
		t.Error("expected non-empty reply for /keyusage")
	}

	// Unknown command
	reply = b.buildReply("/unknown")
	if reply == "" {
		t.Error("expected non-empty reply for unknown command")
	}

	// Plan command
	_ = b.db.SavePlan("pro", types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "anthropic", Model: "claude-3"},
		},
	})
	reply = b.buildReply("/plan pro")
	if !contains(reply, "pro") {
		t.Errorf("expected plan reply to mention 'pro', got %q", reply)
	}

	// Key command
	key := types.APIKey{Key: "sr-buildreply", Name: "buildreply"}
	_ = b.db.CreateAPIKey(key)
	reply = b.buildReply("/key sr-buildreply")
	if !contains(reply, "buildreply") {
		t.Errorf("expected key reply to mention name, got %q", reply)
	}

	// Key command with empty text
	reply = b.buildReply("/key")
	if !contains(reply, "Usage") && !contains(reply, "usage") {
		t.Errorf("expected usage hint for /key without args, got %q", reply)
	}
}

func TestCmdHelp(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	reply := b.cmdHelp()
	if reply == "" {
		t.Fatal("expected non-empty help")
	}
	if !contains(reply, "/health") {
		t.Errorf("expected help to mention /health, got %q", reply)
	}
}

func TestCmdPlans(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	// Seed a plan
	_ = b.db.SavePlan("pro", types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "anthropic", Model: "claude-3"},
		},
	})

	reply := b.cmdPlans()
	if !contains(reply, "pro") {
		t.Errorf("expected plans to mention 'pro', got %q", reply)
	}
}

func TestCmdStatus(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	// Seed a plan with provider
	_ = b.db.SavePlan("default", types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "anthropic", Model: "claude-3"},
		},
	})

	reply := b.cmdStatus()
	if reply == "" {
		t.Fatal("expected non-empty status")
	}
	if !contains(reply, "Smart Router Status") {
		t.Errorf("expected status reply to contain 'Smart Router Status', got %q", reply)
	}

	// With an unhealthy provider (server_error threshold is 2)
	_ = b.health.RecordFailure("anthropic", 500, "internal server error")
	_ = b.health.RecordFailure("anthropic", 500, "internal server error")
	reply = b.cmdStatus()
	if !contains(reply, "Unhealthy Providers") {
		t.Errorf("expected status reply to contain 'Unhealthy Providers', got %q", reply)
	}
	if !contains(reply, "anthropic") {
		t.Errorf("expected status reply to mention unhealthy provider, got %q", reply)
	}
}

func TestCmdKeys(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	_ = b.db.CreateAPIKey(types.APIKey{Key: "sr-key1", Name: "key1"})
	_ = b.db.CreateAPIKey(types.APIKey{Key: "sr-key2", Name: "key2"})

	reply := b.cmdKeys()
	if !contains(reply, "key1") {
		t.Errorf("expected keys to mention 'key1', got %q", reply)
	}
}

func TestResolveKey(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	key := types.APIKey{Key: "sr-resolve-me", Name: "resolve-me"}
	_ = b.db.CreateAPIKey(key)

	// By key
	got, err := b.resolveKey("sr-resolve-me")
	if err != nil {
		t.Fatalf("resolveKey by key: %v", err)
	}
	if got.Key != key.Key {
		t.Errorf("expected key %q, got %q", key.Key, got.Key)
	}

	// By name
	got, err = b.resolveKey("resolve-me")
	if err != nil {
		t.Fatalf("resolveKey by name: %v", err)
	}
	if got.Key != key.Key {
		t.Errorf("expected key %q, got %q", key.Key, got.Key)
	}

	// Not found
	_, err = b.resolveKey("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
}

func TestCmdKey(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	key := types.APIKey{Key: "sr-cmdkey", Name: "cmdkey", Plans: []string{"default"}}
	_ = b.db.CreateAPIKey(key)

	reply := b.cmdKey([]string{"sr-cmdkey"})
	if !contains(reply, "cmdkey") {
		t.Errorf("expected key details to mention name, got %q", reply)
	}

	// No args
	reply = b.cmdKey([]string{})
	if !contains(reply, "Usage") && !contains(reply, "usage") {
		t.Errorf("expected usage hint, got %q", reply)
	}
}

func TestCmdKey_NotFound(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	reply := b.cmdKey([]string{"nonexistent-key"})
	if !contains(reply, "not found") {
		t.Errorf("expected 'not found' message, got %q", reply)
	}
}

func TestCmdKeyUsage(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	key := types.APIKey{Key: "sr-usagekey", Name: "usagekey", Plans: []string{"default"}}
	_ = b.db.CreateAPIKey(key)

	// Record stats
	for i := 0; i < 3; i++ {
		_ = b.db.RecordStat(types.StatRecord{
			Plan:           "default",
			Provider:       "test",
			Model:          "gpt-4",
			ClientKey:      key.Key,
			Status:         "success",
			LatencyMs:      100,
			RequestTokens:  10,
			ResponseTokens: 20,
			TotalTokens:    30,
		})
	}
	b.db.FlushStats()

	reply := b.cmdKeyUsage([]string{"sr-usagekey"})
	if reply == "" {
		t.Fatal("expected non-empty usage reply")
	}

	// No args
	reply = b.cmdKeyUsage([]string{})
	if !contains(reply, "Usage") && !contains(reply, "usage") {
		t.Errorf("expected usage hint, got %q", reply)
	}
}

func TestCmdPlan(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	_ = b.db.SavePlan("pro", types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "anthropic", Model: "claude-3", BaseURL: "https://api.anthropic.com", APIKey: "sk-test"},
		},
	})

	reply := b.cmdPlan([]string{"pro"})
	if !contains(reply, "pro") {
		t.Errorf("expected plan reply to mention 'pro', got %q", reply)
	}

	// No args
	reply = b.cmdPlan([]string{})
	if !contains(reply, "Usage") && !contains(reply, "usage") {
		t.Errorf("expected usage hint, got %q", reply)
	}
}

func TestCmdHealth(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	_ = b.db.SavePlan("default", types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "anthropic", Model: "claude-3"},
		},
	})

	// All plans (no args)
	reply := b.cmdHealth([]string{})
	if reply == "" {
		t.Fatal("expected non-empty health reply")
	}

	// Specific provider
	reply = b.cmdHealth([]string{"anthropic"})
	if reply == "" {
		t.Fatal("expected non-empty health reply for specific provider")
	}

	// Nonexistent provider
	reply = b.cmdHealth([]string{"nonexistent"})
	if reply == "" {
		t.Fatal("expected non-empty health reply for nonexistent provider")
	}
}

func TestCmdHealth_NotFound(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	reply := b.cmdHealth([]string{"nonexistent"})
	if !contains(reply, "unknown") {
		t.Errorf("expected 'unknown' status for nonexistent provider, got %q", reply)
	}
}

func TestCmdStats(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	_ = b.db.SavePlan("default", types.PlanConfig{})
	for i := 0; i < 3; i++ {
		_ = b.db.RecordStat(types.StatRecord{
			Plan:      "default",
			Provider:  "test",
			Status:    "success",
			LatencyMs: 100,
		})
	}
	b.db.FlushStats()

	// No args
	reply := b.cmdStats([]string{})
	if reply == "" {
		t.Fatal("expected non-empty stats reply")
	}

	// With plan argument
	reply = b.cmdStats([]string{"default"})
	if reply == "" {
		t.Fatal("expected non-empty stats reply with plan arg")
	}
}

func TestCmdStats_WithTimeWindow(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	_ = b.db.SavePlan("default", types.PlanConfig{})
	for i := 0; i < 3; i++ {
		_ = b.db.RecordStat(types.StatRecord{
			Plan:      "default",
			Provider:  "test",
			Status:    "success",
			LatencyMs: 100,
		})
	}
	b.db.FlushStats()

	// With time window argument (e.g., "24h")
	reply := b.cmdStats([]string{"default", "test", "24"})
	if reply == "" {
		t.Fatal("expected non-empty stats reply with time window arg")
	}
}

func TestCmdStats_NoArgsDefaultWindow(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	_ = b.db.SavePlan("default", types.PlanConfig{})
	for i := 0; i < 3; i++ {
		_ = b.db.RecordStat(types.StatRecord{
			Plan:      "default",
			Provider:  "test",
			Status:    "success",
			LatencyMs: 100,
		})
	}
	b.db.FlushStats()

	// No args → default window
	reply := b.cmdStats([]string{})
	if reply == "" {
		t.Fatal("expected non-empty stats reply")
	}
	if !contains(reply, "Recent Stats") {
		t.Errorf("expected stats header, got %q", reply)
	}
}

func TestCmdUsage(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	_ = b.db.SavePlan("default", types.PlanConfig{})
	for i := 0; i < 3; i++ {
		_ = b.db.RecordStat(types.StatRecord{
			Plan:      "default",
			Provider:  "test",
			Status:    "success",
			LatencyMs: 100,
		})
	}
	b.db.FlushStats()

	reply := b.cmdUsage([]string{"default"})
	if reply == "" {
		t.Fatal("expected non-empty usage reply")
	}

	// No args → should return usage hint
	reply = b.cmdUsage([]string{})
	if !contains(reply, "Usage") && !contains(reply, "usage") {
		t.Errorf("expected usage hint, got %q", reply)
	}
}

func TestCmdUsage_NoArgsMultipleProviders(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	_ = b.db.SavePlan("default", types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "anthropic", Model: "claude-3"},
			{Name: "openai", Model: "gpt-4"},
		},
	})
	_ = b.db.SavePlan("pro", types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "gemini", Model: "gemini-pro"},
		},
	})

	// No args with multiple providers across plans
	reply := b.cmdUsage([]string{})
	if reply == "" {
		t.Fatal("expected non-empty usage reply")
	}
	if !contains(reply, "anthropic") {
		t.Errorf("expected usage to list anthropic, got %q", reply)
	}
	if !contains(reply, "openai") {
		t.Errorf("expected usage to list openai, got %q", reply)
	}
	if !contains(reply, "gemini") {
		t.Errorf("expected usage to list gemini, got %q", reply)
	}
}

func TestCmdUsage_WithProviderName(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	_ = b.db.SavePlan("default", types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "anthropic", Model: "claude-3"},
		},
	})

	reply := b.cmdUsage([]string{"anthropic"})
	if reply == "" {
		t.Fatal("expected non-empty usage reply")
	}
	if !contains(reply, "anthropic") {
		t.Errorf("expected usage to mention anthropic, got %q", reply)
	}
}

func TestCmdUsage_WithProviderAndCustomWindow(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	_ = b.db.SavePlan("default", types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "anthropic", Model: "claude-3"},
		},
	})

	reply := b.cmdUsage([]string{"anthropic", "7d"})
	if reply == "" {
		t.Fatal("expected non-empty usage reply")
	}
	if !contains(reply, "1 week") && !contains(reply, "7d") {
		t.Errorf("expected usage to mention time window, got %q", reply)
	}
}

func TestCmdUsage_NonexistentProvider(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	reply := b.cmdUsage([]string{"nonexistent"})
	if !contains(reply, "not found") {
		t.Errorf("expected 'not found' message, got %q", reply)
	}
}

func TestCmdTop(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		_ = b.db.RecordStat(types.StatRecord{
			Plan:      "default",
			Provider:  "anthropic",
			Status:    "success",
			LatencyMs: 100,
		})
	}
	b.db.FlushStats()

	reply := b.cmdTop()
	if reply == "" {
		t.Fatal("expected non-empty top reply")
	}
}

func TestCmdFailures(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		_ = b.db.RecordStat(types.StatRecord{
			Plan:      "default",
			Provider:  "anthropic",
			Status:    "error",
			LatencyMs: 100,
		})
	}
	b.db.FlushStats()

	reply := b.cmdFailures()
	if reply == "" {
		t.Fatal("expected non-empty failures reply")
	}
}

func TestResolveProvider(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	_ = b.db.SavePlan("default", types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "anthropic", Model: "claude-3"},
			{Name: "openai", Model: "gpt-4"},
		},
	})

	// Exact name match
	p, ok := b.resolveProvider("anthropic")
	if !ok || p.Name != "anthropic" {
		t.Errorf("expected exact match for anthropic, got %q, ok=%v", p.Name, ok)
	}

	// Partial name match — resolveProvider does exact match only, so no match
	_, ok = b.resolveProvider("nonexistent")
	if ok {
		t.Error("expected no match for nonexistent provider")
	}
}

func TestFormatUsage(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	usage := &db.WeeklyUsage{
		RequestTokens:  1000,
		ResponseTokens: 500,
		RequestCount:   10,
	}
	reply := b.formatUsage("test", usage, "24h")
	if reply == "" {
		t.Fatal("expected non-empty usage format")
	}
	if !contains(reply, "test") {
		t.Errorf("expected usage to mention name, got %q", reply)
	}
}

func TestBuildReply_EdgeCases(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	// Empty message
	reply := b.buildReply("")
	if reply != "" {
		t.Errorf("expected empty reply for empty input, got %q", reply)
	}

	// Very long message (should still parse command)
	longMsg := "/help " + strings.Repeat("a", 5000)
	reply = b.buildReply(longMsg)
	if reply == "" {
		t.Error("expected non-empty reply for long message")
	}

	// Message with markdown characters
	reply = b.buildReply("/help *bold* _italic_ `code`")
	if reply == "" {
		t.Error("expected non-empty reply for markdown message")
	}
}

func TestCmdUsage_DBError(t *testing.T) {
	dir, err := os.MkdirTemp("", "telegram-dberr-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	sqlitePath := dir + "/test.db"
	database, err := db.Open(sqlitePath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	ht, err := health.New(dir + "/health")
	if err != nil {
		database.Close()
		t.Fatalf("open health: %v", err)
	}

	b := &Bot{
		token:  "dummy",
		db:     database,
		health: ht,
	}

	// Close DB and health to force errors
	database.Close()
	ht.Close()

	reply := b.cmdUsage([]string{})
	if !contains(reply, "Error") {
		t.Errorf("expected error message, got %q", reply)
	}
}

func TestCmdStats_Empty(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	reply := b.cmdStats([]string{})
	if !contains(reply, "No stats") {
		t.Errorf("expected 'No stats found' for empty stats, got %q", reply)
	}
}

func TestCmdTop_FewerThanTopN(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	// Only 2 providers worth of stats
	for i := 0; i < 2; i++ {
		_ = b.db.RecordStat(types.StatRecord{
			Plan:      "default",
			Provider:  "provider-a",
			Status:    "success",
			LatencyMs: 100,
		})
	}
	b.db.FlushStats()

	reply := b.cmdTop()
	if reply == "" {
		t.Fatal("expected non-empty top reply")
	}
	if !contains(reply, "provider-a") {
		t.Errorf("expected top to mention provider-a, got %q", reply)
	}
}

func TestCmdFailures_Empty(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	reply := b.cmdFailures()
	if !contains(reply, "No recent failures") {
		t.Errorf("expected 'No recent failures' for empty failures, got %q", reply)
	}
}

func TestCmdKeys_Empty(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	reply := b.cmdKeys()
	if !contains(reply, "No API keys found") {
		t.Errorf("expected 'No API keys found' for empty keys, got %q", reply)
	}
}

func TestCmdKeyUsage_InvalidTimeWindow(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	key := types.APIKey{Key: "sr-usage-invalid", Name: "usage-invalid", Plans: []string{"default"}}
	_ = b.db.CreateAPIKey(key)

	// Invalid time window should fall back to default (30d)
	reply := b.cmdKeyUsage([]string{"sr-usage-invalid", "invalid-window"})
	if reply == "" {
		t.Fatal("expected non-empty usage reply even with invalid window")
	}
}

func TestCmdKeyUsage_DBError(t *testing.T) {
	dir, err := os.MkdirTemp("", "telegram-keyusage-dberr-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	sqlitePath := dir + "/test.db"
	database, err := db.Open(sqlitePath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	ht, err := health.New(dir + "/health")
	if err != nil {
		database.Close()
		t.Fatalf("open health: %v", err)
	}

	b := &Bot{
		token:  "dummy",
		db:     database,
		health: ht,
	}

	key := types.APIKey{Key: "sr-usage-dberr", Name: "usage-dberr", Plans: []string{"default"}}
	_ = b.db.CreateAPIKey(key)

	// Close DB and health to force errors
	database.Close()
	ht.Close()

	reply := b.cmdKeyUsage([]string{"sr-usage-dberr"})
	if !contains(reply, "Error") && !contains(reply, "not found") {
		t.Errorf("expected error or not-found message, got %q", reply)
	}
}

func TestCmdPlan_NotFound(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	reply := b.cmdPlan([]string{"nonexistent-plan"})
	if !contains(reply, "not found") {
		t.Errorf("expected 'not found' message, got %q", reply)
	}
}

func TestCmdHealth_Empty(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	// No plans at all
	reply := b.cmdHealth([]string{})
	if reply == "" {
		t.Fatal("expected non-empty health reply even with no plans")
	}
	if !contains(reply, "Provider Health") {
		t.Errorf("expected health header, got %q", reply)
	}
}

func TestResolveProvider_Unknown(t *testing.T) {
	b, cleanup := setupTestBot(t)
	defer cleanup()

	_, ok := b.resolveProvider("unknown-provider")
	if ok {
		t.Error("expected resolveProvider to return false for unknown provider")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && len(substr) > 0 && findSubstr(s, substr)
}

func findSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestBuildReply_Empty(t *testing.T) {
	bot, cleanup := setupTestBot(t)
	defer cleanup()

	reply := bot.buildReply("")
	if reply != "" {
		t.Errorf("expected empty string, got %q", reply)
	}
}

func TestBuildReply_UnknownCommand(t *testing.T) {
	bot, cleanup := setupTestBot(t)
	defer cleanup()

	reply := bot.buildReply("/unknown-cmd arg1 arg2")
	if reply == "" {
		t.Error("expected a response for unknown command")
	}
	if !strings.Contains(reply, "Unknown command") {
		t.Errorf("expected 'Unknown command', got %q", reply)
	}
}
