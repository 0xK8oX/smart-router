package db

import (
	"os"
	"strings"
	"testing"
	"time"

	"smart-router/internal/types"
)

func setupTestDB(t *testing.T) *DB {
	t.Helper()
	path := "/tmp/test_smart_router_" + t.Name() + ".db"
	_ = os.Remove(path)
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		_ = os.Remove(path)
	})
	return db
}

func TestOpenInvalidPath(t *testing.T) {
	_, err := Open("/dev/null/invalid/path.db")
	if err == nil {
		t.Fatal("expected error for invalid path, got nil")
	}
}

func TestRecordStatBatch_Multiple(t *testing.T) {
	db := setupTestDB(t)

	records := []types.StatRecord{
		{Plan: "pro", Provider: "anthropic", Model: "m1", Status: "success", LatencyMs: 100, RequestTokens: 10, ResponseTokens: 20, TotalTokens: 30},
		{Plan: "pro", Provider: "anthropic", Model: "m2", Status: "error", LatencyMs: 200, RequestTokens: 5, ResponseTokens: 10, TotalTokens: 15},
		{Plan: "free", Provider: "openai", Model: "m3", Status: "success", LatencyMs: 50, RequestTokens: 100, ResponseTokens: 200, TotalTokens: 300},
	}
	if err := db.RecordStatBatch(records); err != nil {
		t.Fatalf("RecordStatBatch failed: %v", err)
	}

	stats, err := db.GetStats("", "", 10)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	// Verify the stats were recorded
	if len(stats) != 3 {
		t.Errorf("expected 3 stats, got %d", len(stats))
	}
}

func TestRecordStatBatch_EmptySlice(t *testing.T) {
	db := setupTestDB(t)
	err := db.RecordStatBatch([]types.StatRecord{})
	if err != nil {
		t.Errorf("expected no error for empty batch, got %v", err)
	}
}

func TestGetStats_LimitBounds(t *testing.T) {
	db := setupTestDB(t)
	db.RecordStatBatch([]types.StatRecord{
		{Plan: "test", Provider: "test", Model: "m", Status: "ok", LatencyMs: 1},
	})

	// Test with limit > maxLimit (10000)
	stats, err := db.GetStats("", "", 50000)
	if err != nil {
		t.Fatalf("GetStats with large limit failed: %v", err)
	}
	if len(stats) != 1 {
		t.Errorf("expected 1 stat, got %d", len(stats))
	}

	// Test with limit <= 0 (defaults to maxLimit)
	stats, err = db.GetStats("", "", 0)
	if err != nil {
		t.Fatalf("GetStats with zero limit failed: %v", err)
	}
	if len(stats) != 1 {
		t.Errorf("expected 1 stat, got %d", len(stats))
	}

	// Test with negative limit (defaults to maxLimit)
	stats, err = db.GetStats("", "", -1)
	if err != nil {
		t.Fatalf("GetStats with negative limit failed: %v", err)
	}
	if len(stats) != 1 {
		t.Errorf("expected 1 stat, got %d", len(stats))
	}
}

func TestRecordStatAsync_ChannelClose(t *testing.T) {
	db := setupTestDB(t)

	// Send a stat asynchronously
	db.RecordStatAsync(types.StatRecord{
		Plan:      "default",
		Provider:  "test",
		Model:     "gpt-4",
		ClientKey: "sr-async-close",
		Status:    "success",
		LatencyMs: 100,
	})

	// Flush and verify it was recorded
	db.FlushStats()
	stats, err := db.GetStats("default", "test", 10)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	found := false
	for _, s := range stats {
		if s.ClientKey == "sr-async-close" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected async stat to be recorded")
	}
}

func TestSavePlan_ExecError(t *testing.T) {
	// Use a temp dir and open a separate DB so we can close it without
	// interfering with setupTestDB's t.Cleanup.
	dir := t.TempDir()
	db, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	_ = db.Close()

	err = db.SavePlan("pro", types.PlanConfig{Strategy: "x"})
	if err == nil {
		t.Fatal("expected error saving plan on closed DB, got nil")
	}
}

func TestGetPlan_NotFound(t *testing.T) {
	db := setupTestDB(t)
	_, err := db.GetPlan("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent plan, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected error to contain 'not found', got %v", err)
	}
}

func TestListPlans_QueryError(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	_ = db.Close()

	_, err = db.ListPlans()
	if err == nil {
		t.Fatal("expected error listing plans on closed DB, got nil")
	}
}

func TestDeletePlan_ExecError(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	_ = db.Close()

	err = db.DeletePlan("pro")
	if err == nil {
		t.Fatal("expected error deleting plan on closed DB, got nil")
	}
}

func TestCreateAPIKey_ExecError(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	_ = db.Close()

	key := types.APIKey{Key: "sr-test", Name: "test", Plans: []string{"default"}, CreatedAt: time.Now().Unix()}
	err = db.CreateAPIKey(key)
	if err == nil {
		t.Fatal("expected error creating API key on closed DB, got nil")
	}
}

func TestGetAPIKey_NotFound(t *testing.T) {
	db := setupTestDB(t)
	_, err := db.GetAPIKey("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent key, got nil")
	}
}

func TestGetAPIKeyByName_NotFound(t *testing.T) {
	db := setupTestDB(t)
	_, err := db.GetAPIKeyByName("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent key name, got nil")
	}
}

func TestListAPIKeys_QueryError(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	_ = db.Close()

	_, err = db.ListAPIKeys()
	if err == nil {
		t.Fatal("expected error listing API keys on closed DB, got nil")
	}
}

func TestUpdateAPIKey_ExecError(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	_ = db.Close()

	err = db.UpdateAPIKey("nonexistent", types.APIKey{Name: "x"})
	if err == nil {
		t.Fatal("expected error updating API key on closed DB, got nil")
	}
}

func TestDeleteAPIKey_ExecError(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	_ = db.Close()

	err = db.DeleteAPIKey("nonexistent")
	if err == nil {
		t.Fatal("expected error deleting API key on closed DB, got nil")
	}
}

func TestCreateKeyGroup_ExecError(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	_ = db.Close()

	_, err = db.CreateKeyGroup(types.KeyGroup{Name: "x"})
	if err == nil {
		t.Fatal("expected error creating key group on closed DB, got nil")
	}
}

func TestGetKeyGroup_NotFound(t *testing.T) {
	db := setupTestDB(t)
	_, err := db.GetKeyGroup(99999)
	if err == nil {
		t.Fatal("expected error for nonexistent key group, got nil")
	}
}

func TestUpdateKeyGroup_ExecError(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	_ = db.Close()

	err = db.UpdateKeyGroup(99999, types.KeyGroup{Name: "x"})
	if err == nil {
		t.Fatal("expected error updating key group on closed DB, got nil")
	}
}

func TestDeleteKeyGroup_ExecError(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	_ = db.Close()

	err = db.DeleteKeyGroup(99999)
	if err == nil {
		t.Fatal("expected error deleting key group on closed DB, got nil")
	}
}

func TestListKeyGroups_QueryError(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	_ = db.Close()

	_, err = db.ListKeyGroups()
	if err == nil {
		t.Fatal("expected error listing key groups on closed DB, got nil")
	}
}

func TestRecordKeyAudit_ExecError(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	_ = db.Close()

	err = db.RecordAudit("action", "target", "actor", "details")
	if err == nil {
		t.Fatal("expected error recording audit on closed DB, got nil")
	}
}

func TestGetKeyMonthlyUsage_NoStats(t *testing.T) {
	db := setupTestDB(t)

	key := types.APIKey{Key: "sr-no-stats", Name: "no-stats", Plans: []string{"default"}}
	_ = db.CreateAPIKey(key)

	now := time.Now()
	usage, err := db.GetKeyMonthlyUsage(key.Key, now.Year(), int(now.Month()))
	if err != nil {
		t.Fatalf("GetKeyMonthlyUsage failed: %v", err)
	}
	if usage.RequestCount != 0 {
		t.Errorf("expected request_count=0, got %d", usage.RequestCount)
	}
	if usage.RequestTokens != 0 {
		t.Errorf("expected request_tokens=0, got %d", usage.RequestTokens)
	}
	if usage.ResponseTokens != 0 {
		t.Errorf("expected response_tokens=0, got %d", usage.ResponseTokens)
	}
}

func TestGetKeyMonthlyCost_NoPricing(t *testing.T) {
	db := setupTestDB(t)

	key := types.APIKey{Key: "sr-cost-nopricing", Name: "cost-nopricing", Plans: []string{"default"}}
	_ = db.CreateAPIKey(key)

	now := time.Now()
	_ = db.RecordStat(types.StatRecord{
		Plan:           "default",
		Provider:       "test",
		Model:          "unknown-model",
		ClientKey:      key.Key,
		Status:         "success",
		LatencyMs:      100,
		RequestTokens:  1000,
		ResponseTokens: 500,
		TotalTokens:    1500,
	})
	db.FlushStats()

	cost, err := db.GetKeyMonthlyCost(key.Key, now.Year(), int(now.Month()))
	if err != nil {
		t.Fatalf("GetKeyMonthlyCost failed: %v", err)
	}
	if cost != 0 {
		t.Errorf("expected cost=0 when no pricing data, got %f", cost)
	}
}

func TestGetKeyMonthlyCost_WithPricing(t *testing.T) {
	db := setupTestDB(t)

	key := types.APIKey{Key: "sr-cost-pricing", Name: "cost-pricing", Plans: []string{"default"}}
	_ = db.CreateAPIKey(key)

	_ = db.SetModelPricing("gpt-4", 0.03, 0.06)

	now := time.Now()
	_ = db.RecordStat(types.StatRecord{
		Plan:           "default",
		Provider:       "test",
		Model:          "gpt-4",
		ClientKey:      key.Key,
		Status:         "success",
		LatencyMs:      100,
		RequestTokens:  1000,
		ResponseTokens: 500,
		TotalTokens:    1500,
	})
	db.FlushStats()

	cost, err := db.GetKeyMonthlyCost(key.Key, now.Year(), int(now.Month()))
	if err != nil {
		t.Fatalf("GetKeyMonthlyCost failed: %v", err)
	}
	// (1000/1000)*0.03 + (500/1000)*0.06 = 0.03 + 0.03 = 0.06
	if cost != 0.06 {
		t.Errorf("expected cost=0.06, got %f", cost)
	}
}

func TestDeleteAPIKey_NotFound(t *testing.T) {
	db := setupTestDB(t)
	if err := db.DeleteAPIKey("nonexistent"); err == nil {
		t.Fatal("expected error for deleting nonexistent key")
	}
}

func TestUpdateAPIKey_NotFound(t *testing.T) {
	db := setupTestDB(t)
	if err := db.UpdateAPIKey("nonexistent", types.APIKey{Name: "x"}); err == nil {
		t.Fatal("expected error for updating nonexistent key")
	}
}

func TestDeleteKeyGroup_NotFound(t *testing.T) {
	db := setupTestDB(t)
	if err := db.DeleteKeyGroup(99999); err == nil {
		t.Fatal("expected error for deleting nonexistent group")
	}
}

func TestUpdateKeyGroup_NotFound(t *testing.T) {
	db := setupTestDB(t)
	if err := db.UpdateKeyGroup(99999, types.KeyGroup{Name: "x"}); err == nil {
		t.Fatal("expected error for updating nonexistent group")
	}
}

func TestRecordAudit_MultipleInOrder(t *testing.T) {
	db := setupTestDB(t)

	actions := []string{"create_key", "update_key", "delete_key", "rotate_key"}
	for _, action := range actions {
		if err := db.RecordAudit(action, "sr-123", "admin", action+" detail"); err != nil {
			t.Fatalf("RecordAudit failed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	logs, err := db.ListAuditLogs(10)
	if err != nil {
		t.Fatalf("ListAuditLogs failed: %v", err)
	}
	if len(logs) != 4 {
		t.Fatalf("expected 4 audit logs, got %d", len(logs))
	}

	// ListAuditLogs orders by created_at DESC, so reverse order
	for i := 0; i < len(actions); i++ {
		expected := actions[len(actions)-1-i]
		if logs[i]["action"] != expected {
			t.Errorf("log[%d].action = %q, want %q", i, logs[i]["action"], expected)
		}
	}
}

func TestRecordAndQueryStats(t *testing.T) {
	db := setupTestDB(t)

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
		ClientKey:      "sr-test",
	}

	if err := db.RecordStat(record); err != nil {
		t.Fatalf("RecordStat failed: %v", err)
	}
	db.FlushStats()

	stats, err := db.GetStats("pro", "anthropic", 10)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(stats))
	}

	s := stats[0]
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
	if s.ClientKey != record.ClientKey {
		t.Errorf("client_key mismatch: got %q, want %q", s.ClientKey, record.ClientKey)
	}
}

func TestGetStatsFilterByPlanAndProvider(t *testing.T) {
	db := setupTestDB(t)

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
	db.FlushStats()

	stats, err := db.GetStats("pro", "", 10)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats for plan=pro, got %d", len(stats))
	}

	stats, err = db.GetStats("", "anthropic", 10)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats for provider=anthropic, got %d", len(stats))
	}

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

	stats, err = db.GetStats("", "", 2)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats with limit=2, got %d", len(stats))
	}
}

func TestAPIKeyCRUD(t *testing.T) {
	db := setupTestDB(t)

	key := types.APIKey{
		Key:                 "sr-test123",
		Name:                "test-key",
		Plans:               []string{"default", "pro"},
		Models:              []string{"gpt-4", "claude-3"},
		RateLimitRPM:        10,
		RateLimitRPD:        100,
		MonthlyTokenLimit:   100000,
		MonthlyRequestLimit: 1000,
		Disabled:            false,
		CreatedAt:           time.Now().Unix(),
	}

	// Create
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("CreateAPIKey failed: %v", err)
	}

	// Count
	count, err := db.CountAPIKeys()
	if err != nil {
		t.Fatalf("CountAPIKeys failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected count=1, got %d", count)
	}

	// Get
	got, err := db.GetAPIKey(key.Key)
	if err != nil {
		t.Fatalf("GetAPIKey failed: %v", err)
	}
	if got.Key != key.Key {
		t.Errorf("key mismatch: got %q, want %q", got.Key, key.Key)
	}
	if got.Name != key.Name {
		t.Errorf("name mismatch: got %q, want %q", got.Name, key.Name)
	}
	if len(got.Plans) != 2 {
		t.Errorf("plans mismatch: got %v", got.Plans)
	}
	if len(got.Models) != 2 {
		t.Errorf("models mismatch: got %v", got.Models)
	}

	// Get by name
	gotByName, err := db.GetAPIKeyByName(key.Name)
	if err != nil {
		t.Fatalf("GetAPIKeyByName failed: %v", err)
	}
	if gotByName.Key != key.Key {
		t.Errorf("GetAPIKeyByName mismatch: got %q, want %q", gotByName.Key, key.Key)
	}

	// List
	keys, err := db.ListAPIKeys()
	if err != nil {
		t.Fatalf("ListAPIKeys failed: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}

	// Update
	updates := types.APIKey{
		Name:      "updated-name",
		Plans:     []string{"premium"},
		Models:    []string{"gpt-4-turbo"},
		Disabled:  true,
		AllowedIPs: []string{"10.0.0.0/8"},
	}
	if err := db.UpdateAPIKey(key.Key, updates); err != nil {
		t.Fatalf("UpdateAPIKey failed: %v", err)
	}

	got, err = db.GetAPIKey(key.Key)
	if err != nil {
		t.Fatalf("GetAPIKey after update failed: %v", err)
	}
	if got.Name != "updated-name" {
		t.Errorf("name not updated: got %q", got.Name)
	}
	if len(got.Plans) != 1 || got.Plans[0] != "premium" {
		t.Errorf("plans not updated: got %v", got.Plans)
	}
	if !got.Disabled {
		t.Errorf("disabled not updated: got %v", got.Disabled)
	}
	if len(got.AllowedIPs) != 1 || got.AllowedIPs[0] != "10.0.0.0/8" {
		t.Errorf("allowed_ips not updated: got %v", got.AllowedIPs)
	}

	// Delete
	if err := db.DeleteAPIKey(key.Key); err != nil {
		t.Fatalf("DeleteAPIKey failed: %v", err)
	}

	_, err = db.GetAPIKey(key.Key)
	if err == nil {
		t.Fatal("expected key to be deleted")
	}
}

func TestKeyGroupCRUD(t *testing.T) {
	db := setupTestDB(t)

	group := types.KeyGroup{
		Name:                "test-group",
		MonthlyTokenLimit:   100000,
		MonthlyRequestLimit: 1000,
		MonthlyBudgetLimit:  50.0,
	}

	// Create
	id, err := db.CreateKeyGroup(group)
	if err != nil {
		t.Fatalf("CreateKeyGroup failed: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero group ID")
	}

	// Get
	got, err := db.GetKeyGroup(id)
	if err != nil {
		t.Fatalf("GetKeyGroup failed: %v", err)
	}
	if got.Name != group.Name {
		t.Errorf("name mismatch: got %q, want %q", got.Name, group.Name)
	}

	// List
	groups, err := db.ListKeyGroups()
	if err != nil {
		t.Fatalf("ListKeyGroups failed: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}

	// Update
	updates := types.KeyGroup{
		Name:                "updated-group",
		MonthlyTokenLimit:   200000,
		MonthlyRequestLimit: 2000,
		MonthlyBudgetLimit:  100.0,
	}
	if err := db.UpdateKeyGroup(id, updates); err != nil {
		t.Fatalf("UpdateKeyGroup failed: %v", err)
	}

	got, err = db.GetKeyGroup(id)
	if err != nil {
		t.Fatalf("GetKeyGroup after update failed: %v", err)
	}
	if got.Name != "updated-group" {
		t.Errorf("name not updated: got %q", got.Name)
	}
	if got.MonthlyTokenLimit != 200000 {
		t.Errorf("token limit not updated: got %d", got.MonthlyTokenLimit)
	}

	// Delete
	if err := db.DeleteKeyGroup(id); err != nil {
		t.Fatalf("DeleteKeyGroup failed: %v", err)
	}

	_, err = db.GetKeyGroup(id)
	if err == nil {
		t.Fatal("expected group to be deleted")
	}
}

func TestUsageQueries(t *testing.T) {
	db := setupTestDB(t)

	key := types.APIKey{
		Key:   "sr-usage-test",
		Name:  "usage-test",
		Plans: []string{"default"},
	}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("CreateAPIKey failed: %v", err)
	}

	now := time.Now()
	// Record stats for this month
	for i := 0; i < 5; i++ {
		_ = db.RecordStat(types.StatRecord{
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
	db.FlushStats()

	// GetKeyMonthlyUsage
	monthly, err := db.GetKeyMonthlyUsage(key.Key, now.Year(), int(now.Month()))
	if err != nil {
		t.Fatalf("GetKeyMonthlyUsage failed: %v", err)
	}
	if monthly.RequestCount != 5 {
		t.Errorf("expected request_count=5, got %d", monthly.RequestCount)
	}
	if monthly.RequestTokens != 50 {
		t.Errorf("expected request_tokens=50, got %d", monthly.RequestTokens)
	}
	if monthly.ResponseTokens != 100 {
		t.Errorf("expected response_tokens=100, got %d", monthly.ResponseTokens)
	}

	// GetKeyUsageSince (last 7 days)
	weekly, err := db.GetKeyUsageSince(key.Key, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("GetKeyUsageSince failed: %v", err)
	}
	if weekly.RequestCount != 5 {
		t.Errorf("expected weekly request_count=5, got %d", weekly.RequestCount)
	}

	// GetUsageSince without key filter
	allWeekly, err := db.GetUsageSince("", now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("GetUsageSince failed: %v", err)
	}
	if allWeekly.RequestCount != 5 {
		t.Errorf("expected all request_count=5, got %d", allWeekly.RequestCount)
	}

	// GetWeeklyUsage
	wk, err := db.GetWeeklyUsage("")
	if err != nil {
		t.Fatalf("GetWeeklyUsage failed: %v", err)
	}
	if wk.RequestCount != 5 {
		t.Errorf("expected weekly request_count=5, got %d", wk.RequestCount)
	}
}

func TestAuditLog(t *testing.T) {
	db := setupTestDB(t)

	// Record audit entries
	if err := db.RecordAudit("create_key", "sr-123", "admin", "created test key"); err != nil {
		t.Fatalf("RecordAudit failed: %v", err)
	}
	if err := db.RecordAudit("update_key", "sr-123", "admin", "updated test key"); err != nil {
		t.Fatalf("RecordAudit failed: %v", err)
	}
	if err := db.RecordAudit("delete_key", "sr-123", "admin", "deleted test key"); err != nil {
		t.Fatalf("RecordAudit failed: %v", err)
	}

	// List audit logs
	logs, err := db.ListAuditLogs(10)
	if err != nil {
		t.Fatalf("ListAuditLogs failed: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 audit logs, got %d", len(logs))
	}

	// Limit
	logs, err = db.ListAuditLogs(2)
	if err != nil {
		t.Fatalf("ListAuditLogs failed: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 audit logs with limit, got %d", len(logs))
	}
}

func TestModelPricing(t *testing.T) {
	db := setupTestDB(t)

	// Set pricing
	if err := db.SetModelPricing("gpt-4", 0.03, 0.06); err != nil {
		t.Fatalf("SetModelPricing failed: %v", err)
	}
	if err := db.SetModelPricing("claude-3", 0.015, 0.075); err != nil {
		t.Fatalf("SetModelPricing failed: %v", err)
	}

	// Get pricing
	input, output, err := db.GetModelPricing("gpt-4")
	if err != nil {
		t.Fatalf("GetModelPricing failed: %v", err)
	}
	if input != 0.03 {
		t.Errorf("expected input=0.03, got %f", input)
	}
	if output != 0.06 {
		t.Errorf("expected output=0.06, got %f", output)
	}

	// Update pricing
	if err := db.SetModelPricing("gpt-4", 0.025, 0.05); err != nil {
		t.Fatalf("SetModelPricing update failed: %v", err)
	}
	input, output, err = db.GetModelPricing("gpt-4")
	if err != nil {
		t.Fatalf("GetModelPricing after update failed: %v", err)
	}
	if input != 0.025 {
		t.Errorf("expected updated input=0.025, got %f", input)
	}

	// Nonexistent model returns zero prices, no error
	input, output, err = db.GetModelPricing("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error for nonexistent model: %v", err)
	}
	if input != 0 || output != 0 {
		t.Fatalf("expected zero prices for nonexistent model, got %f, %f", input, output)
	}
}

func TestGetKeyMonthlyCost(t *testing.T) {
	db := setupTestDB(t)

	key := types.APIKey{
		Key:   "sr-cost-test",
		Name:  "cost-test",
		Plans: []string{"default"},
	}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("CreateAPIKey failed: %v", err)
	}

	// Set pricing
	_ = db.SetModelPricing("gpt-4", 0.03, 0.06)

	now := time.Now()
	// Record stats with tokens
	_ = db.RecordStat(types.StatRecord{
		Plan:           "default",
		Provider:       "test",
		Model:          "gpt-4",
		ClientKey:      key.Key,
		Status:         "success",
		LatencyMs:      100,
		RequestTokens:  1000,
		ResponseTokens: 500,
		TotalTokens:    1500,
	})
	db.FlushStats()

	cost, err := db.GetKeyMonthlyCost(key.Key, now.Year(), int(now.Month()))
	if err != nil {
		t.Fatalf("GetKeyMonthlyCost failed: %v", err)
	}
	// Expected: (1000/1000)*0.03 + (500/1000)*0.06 = 0.03 + 0.03 = 0.06
	if cost != 0.06 {
		t.Errorf("expected cost=0.06, got %f", cost)
	}
}

func TestGroupMonthlyUsage(t *testing.T) {
	db := setupTestDB(t)

	group := types.KeyGroup{Name: "test-group"}
	groupID, _ := db.CreateKeyGroup(group)

	key := types.APIKey{
		Key:     "sr-group-test",
		Name:    "group-test",
		Plans:   []string{"default"},
		GroupID: &groupID,
	}
	_ = db.CreateAPIKey(key)

	now := time.Now()
	for i := 0; i < 3; i++ {
		_ = db.RecordStat(types.StatRecord{
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
	db.FlushStats()

	usage, err := db.GetGroupMonthlyUsage(groupID, now.Year(), int(now.Month()))
	if err != nil {
		t.Fatalf("GetGroupMonthlyUsage failed: %v", err)
	}
	if usage.RequestCount != 3 {
		t.Errorf("expected request_count=3, got %d", usage.RequestCount)
	}
	if usage.RequestTokens != 30 {
		t.Errorf("expected request_tokens=30, got %d", usage.RequestTokens)
	}
}

func TestUpdateKeyLastUsed(t *testing.T) {
	db := setupTestDB(t)

	key := types.APIKey{
		Key:       "sr-lastused-test",
		Name:      "lastused-test",
		Plans:     []string{"default"},
		CreatedAt: time.Now().Unix(),
	}
	_ = db.CreateAPIKey(key)

	// Update last_used
	ts := time.Now().Unix()
	if err := db.UpdateKeyLastUsed(key.Key); err != nil {
		t.Fatalf("UpdateKeyLastUsed failed: %v", err)
	}

	got, _ := db.GetAPIKey(key.Key)
	if got.LastUsedAt == nil || *got.LastUsedAt == 0 {
		t.Fatal("expected LastUsedAt to be set")
	}

	// Update with specific time
	if err := db.UpdateKeyLastUsedWithTime(key.Key, ts); err != nil {
		t.Fatalf("UpdateKeyLastUsedWithTime failed: %v", err)
	}

	got, _ = db.GetAPIKey(key.Key)
	if got.LastUsedAt == nil || *got.LastUsedAt != ts {
		t.Fatalf("expected LastUsedAt=%d, got %v", ts, got.LastUsedAt)
	}
}

func TestPlanCRUD(t *testing.T) {
	db := setupTestDB(t)

	plan := types.PlanConfig{
		Strategy: "round_robin",
		Providers: []types.ProviderConfig{
			{Name: "openai", BaseURL: "https://api.openai.com", Model: "gpt-4", Format: "openai", Timeout: 30, APIKey: "sk-test"},
		},
	}

	// Save
	if err := db.SavePlan("pro", plan); err != nil {
		t.Fatalf("SavePlan failed: %v", err)
	}

	// Get
	got, err := db.GetPlan("pro")
	if err != nil {
		t.Fatalf("GetPlan failed: %v", err)
	}
	if got.Strategy != plan.Strategy {
		t.Errorf("strategy mismatch: got %q, want %q", got.Strategy, plan.Strategy)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(got.Providers))
	}
	if got.Providers[0].Name != "openai" {
		t.Errorf("provider name mismatch: got %q, want %q", got.Providers[0].Name, "openai")
	}

	// List
	plans, err := db.ListPlans()
	if err != nil {
		t.Fatalf("ListPlans failed: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}
	if p, ok := plans["pro"]; !ok {
		t.Fatal("expected plan 'pro' in list")
	} else if p.Strategy != "round_robin" {
		t.Errorf("listed plan strategy mismatch: got %q", p.Strategy)
	}

	// Update
	plan.Strategy = "weighted_round_robin"
	plan.Providers[0].Weight = 3
	if err := db.SavePlan("pro", plan); err != nil {
		t.Fatalf("SavePlan update failed: %v", err)
	}
	got, err = db.GetPlan("pro")
	if err != nil {
		t.Fatalf("GetPlan after update failed: %v", err)
	}
	if got.Strategy != "weighted_round_robin" {
		t.Errorf("strategy not updated: got %q", got.Strategy)
	}
	if got.Providers[0].Weight != 3 {
		t.Errorf("weight not updated: got %d", got.Providers[0].Weight)
	}

	// Delete
	if err := db.DeletePlan("pro"); err != nil {
		t.Fatalf("DeletePlan failed: %v", err)
	}

	_, err = db.GetPlan("pro")
	if err == nil {
		t.Fatal("expected plan to be deleted")
	}
}

func TestRecordStatAsync(t *testing.T) {
	db := setupTestDB(t)

	done := make(chan struct{})
	go func() {
		// Should not block even when called rapidly
		for i := 0; i < 10; i++ {
			db.RecordStatAsync(types.StatRecord{
				Plan:           "default",
				Provider:       "test",
				Model:          "gpt-4",
				KeyMask:        "sk-test",
				ClientKey:      "sr-async",
				Status:         "success",
				LatencyMs:      100,
				RequestTokens:  10,
				ResponseTokens: 20,
				TotalTokens:    30,
			})
		}
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("RecordStatAsync blocked")
	}

	db.FlushStats()

	// Allow a brief moment for any in-flight async insert to complete
	time.Sleep(100 * time.Millisecond)

	stats, err := db.GetStats("default", "test", 20)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if len(stats) == 0 {
		t.Fatalf("expected some stats, got %d", len(stats))
	}
}

func TestGetUsageSinceForPlan(t *testing.T) {
	db := setupTestDB(t)

	now := time.Now()
	for i := 0; i < 3; i++ {
		_ = db.RecordStat(types.StatRecord{
			Plan:           "plan-a",
			Provider:       "test",
			Model:          "gpt-4",
			KeyMask:        "mask-a",
			ClientKey:      "sr-a",
			Status:         "success",
			LatencyMs:      100,
			RequestTokens:  10,
			ResponseTokens: 20,
			TotalTokens:    30,
		})
	}
	for i := 0; i < 2; i++ {
		_ = db.RecordStat(types.StatRecord{
			Plan:           "plan-b",
			Provider:       "test",
			Model:          "gpt-4",
			KeyMask:        "mask-a",
			ClientKey:      "sr-b",
			Status:         "success",
			LatencyMs:      100,
			RequestTokens:  5,
			ResponseTokens: 10,
			TotalTokens:    15,
		})
	}
	db.FlushStats()

	usage, err := db.GetUsageSinceForPlan("plan-a", "mask-a", now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("GetUsageSinceForPlan failed: %v", err)
	}
	if usage.RequestCount != 3 {
		t.Errorf("expected request_count=3, got %d", usage.RequestCount)
	}
	if usage.RequestTokens != 30 {
		t.Errorf("expected request_tokens=30, got %d", usage.RequestTokens)
	}
	if usage.ResponseTokens != 60 {
		t.Errorf("expected response_tokens=60, got %d", usage.ResponseTokens)
	}

	// Different plan should return different counts
	usageB, err := db.GetUsageSinceForPlan("plan-b", "mask-a", now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("GetUsageSinceForPlan(plan-b) failed: %v", err)
	}
	if usageB.RequestCount != 2 {
		t.Errorf("expected request_count=2 for plan-b, got %d", usageB.RequestCount)
	}
}

func TestGetWeeklyUsageForPlan(t *testing.T) {
	db := setupTestDB(t)

	for i := 0; i < 4; i++ {
		_ = db.RecordStat(types.StatRecord{
			Plan:           "weekly-plan",
			Provider:       "test",
			Model:          "gpt-4",
			KeyMask:        "mask-weekly",
			ClientKey:      "sr-weekly",
			Status:         "success",
			LatencyMs:      100,
			RequestTokens:  10,
			ResponseTokens: 20,
			TotalTokens:    30,
		})
	}
	db.FlushStats()

	usage, err := db.GetWeeklyUsageForPlan("weekly-plan", "mask-weekly")
	if err != nil {
		t.Fatalf("GetWeeklyUsageForPlan failed: %v", err)
	}
	if usage.RequestCount != 4 {
		t.Errorf("expected request_count=4, got %d", usage.RequestCount)
	}
	if usage.RequestTokens != 40 {
		t.Errorf("expected request_tokens=40, got %d", usage.RequestTokens)
	}
	if usage.ResponseTokens != 80 {
		t.Errorf("expected response_tokens=80, got %d", usage.ResponseTokens)
	}
}

func TestExtractSource(t *testing.T) {
	tests := []struct {
		ua   string
		want string
	}{
		{"claude-cli/2.1.143 (external, cli)", "claude-code"},
		{"Claude-Code/1.0.0", "claude-code"},
		{"hermes-agent/1.0.0", "hermes"},
		{"OpenAI/Python 2.32.0", "openai"},
		{"python-httpx/0.28.1", "python"},
		{"curl/8.4.0", "curl"},
		{"Mozilla/5.0", "other"},
		{"", "unknown"},
	}
	for _, tt := range tests {
		got := ExtractSource(tt.ua)
		if got != tt.want {
			t.Errorf("ExtractSource(%q) = %q, want %q", tt.ua, got, tt.want)
		}
	}
}
