package db

import (
	"os"
	"testing"

	"smart-router/internal/types"
)

func TestRecordAndQueryStats(t *testing.T) {
	// Open temp DB
	path := "/tmp/test_smart_router_stats.db"
	_ = os.Remove(path)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() {
		_ = db.Close()
		_ = os.Remove(path)
	}()

	// Record a stat
	record := types.StatRecord{
		Plan:           "pro",
		Provider:       "anthropic",
		Model:          "claude-sonnet-4",
		KeyMask:        "sk-ant...abc1",
		RequestTokens:  100,
		ResponseTokens: 200,
		TotalTokens:    300,
		Status:         "success",
		LatencyMs:      1234,
		IsStreaming:    true,
		TargetProvider: "anthropic",
	}

	if err := db.RecordStat(record); err != nil {
		t.Fatalf("RecordStat failed: %v", err)
	}

	// Query it back
	stats, err := db.GetStats("pro", "anthropic", 10)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if len(stats) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(stats))
	}

	s := stats[0]

	// Verify fields match
	if s.Plan != record.Plan {
		t.Errorf("plan mismatch: got %q, want %q", s.Plan, record.Plan)
	}
	if s.Provider != record.Provider {
		t.Errorf("provider mismatch: got %q, want %q", s.Provider, record.Provider)
	}
	if s.Model != record.Model {
		t.Errorf("model mismatch: got %q, want %q", s.Model, record.Model)
	}
	if s.KeyMask != record.KeyMask {
		t.Errorf("key_mask mismatch: got %q, want %q", s.KeyMask, record.KeyMask)
	}
	if s.RequestTokens != record.RequestTokens {
		t.Errorf("request_tokens mismatch: got %d, want %d", s.RequestTokens, record.RequestTokens)
	}
	if s.ResponseTokens != record.ResponseTokens {
		t.Errorf("response_tokens mismatch: got %d, want %d", s.ResponseTokens, record.ResponseTokens)
	}
	if s.TotalTokens != record.TotalTokens {
		t.Errorf("total_tokens mismatch: got %d, want %d", s.TotalTokens, record.TotalTokens)
	}
	if s.Status != record.Status {
		t.Errorf("status mismatch: got %q, want %q", s.Status, record.Status)
	}
	if s.LatencyMs != record.LatencyMs {
		t.Errorf("latency_ms mismatch: got %d, want %d", s.LatencyMs, record.LatencyMs)
	}
	if s.IsStreaming != record.IsStreaming {
		t.Errorf("is_streaming mismatch: got %v, want %v", s.IsStreaming, record.IsStreaming)
	}
	if s.TargetProvider != record.TargetProvider {
		t.Errorf("target_provider mismatch: got %q, want %q", s.TargetProvider, record.TargetProvider)
	}
}

func TestGetStatsFilterByPlanAndProvider(t *testing.T) {
	path := "/tmp/test_smart_router_filter.db"
	_ = os.Remove(path)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() {
		_ = db.Close()
		_ = os.Remove(path)
	}()

	// Insert stats for different plans/providers
	records := []types.StatRecord{
		{Plan: "pro", Provider: "anthropic", Model: "m1", Status: "success", LatencyMs: 100},
		{Plan: "pro", Provider: "openai", Model: "m2", Status: "success", LatencyMs: 200},
		{Plan: "free", Provider: "anthropic", Model: "m3", Status: "error", LatencyMs: 300},
	}
	for _, r := range records {
		if err := db.RecordStat(r); err != nil {
			t.Fatalf("RecordStat failed: %v", err)
		}
	}

	// Filter by plan only (provider = "")
	stats, err := db.GetStats("pro", "", 10)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats for plan=pro, got %d", len(stats))
	}

	// Filter by provider only (plan = "")
	stats, err = db.GetStats("", "anthropic", 10)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats for provider=anthropic, got %d", len(stats))
	}

	// Filter by both
	stats, err = db.GetStats("free", "anthropic", 10)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat for plan=free provider=anthropic, got %d", len(stats))
	}
	if stats[0].LatencyMs != 300 {
		t.Errorf("expected latency 300, got %d", stats[0].LatencyMs)
	}

	// Limit
	stats, err = db.GetStats("", "", 2)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats with limit=2, got %d", len(stats))
	}
}
