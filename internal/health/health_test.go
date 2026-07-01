package health

import (
	"os"
	"testing"
	"time"
)

func setupTestHealth(t *testing.T) (*HealthTracker, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "health-test-*")
	if err != nil {
		t.Fatal(err)
	}
	ht, err := New(dir)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	cleanup := func() {
		ht.Close()
		os.RemoveAll(dir)
	}
	return ht, cleanup
}

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		status  int
		message string
		want    string
	}{
		{401, "unauthorized", "auth"},
		{402, "insufficient quota", "quota"},
		{0, "authentication failed", "auth"},
		{0, "quota exceeded", "quota"},
		{0, "credit exhausted", "quota"},
		{0, "billing issue", "quota"},
		{400, "InvalidSubscription: does not have a valid CodingPlan subscription", "quota"},
		{400, "your subscription has expired", "quota"},
		{429, "too many requests", "rate_limit"},
		{0, "rate limit hit", "rate_limit"},
		{500, "internal server error", "server_error"},
		{599, "bad gateway", "server_error"},
		{0, "connection reset", "connection"},
		{0, "connection refused", "connection"},
		{0, "request timeout", "timeout"},
		{400, "invalid params", "invalid_request"},
		{422, "unprocessable entity", "invalid_request"},
		{0, "context canceled", ""},
		{0, "context deadline exceeded", "timeout"},
		{0, "something weird", "unknown"},
	}

	for _, tt := range tests {
		got := ClassifyFailure(tt.status, tt.message)
		if got != tt.want {
			t.Errorf("ClassifyFailure(%d, %q) = %q, want %q", tt.status, tt.message, got, tt.want)
		}
	}
}

func TestGetHealth(t *testing.T) {
	ht, cleanup := setupTestHealth(t)
	defer cleanup()

	// Provider never recorded → empty ProviderHealth (status "")
	h, err := ht.GetHealth("never-recorded")
	if err != nil {
		t.Fatalf("GetHealth error: %v", err)
	}
	if h.Status != "" {
		t.Errorf("expected empty status for unrecorded provider, got %q", h.Status)
	}

	// Record success, then get health → status "healthy"
	if err := ht.RecordSuccess("openai"); err != nil {
		t.Fatalf("RecordSuccess error: %v", err)
	}
	h, err = ht.GetHealth("openai")
	if err != nil {
		t.Fatalf("GetHealth error: %v", err)
	}
	if h.Status != "healthy" {
		t.Errorf("expected status healthy, got %q", h.Status)
	}
	if h.SuccessCount != 1 {
		t.Errorf("expected successCount 1, got %d", h.SuccessCount)
	}

	// Record failure on a fresh provider → status "unhealthy" (with cooldown info)
	// server_error threshold is 2, so record twice to trigger unhealthy
	if err := ht.RecordFailure("openai-fail", 500, "internal server error"); err != nil {
		t.Fatalf("RecordFailure error: %v", err)
	}
	if err := ht.RecordFailure("openai-fail", 500, "internal server error"); err != nil {
		t.Fatalf("RecordFailure error: %v", err)
	}
	h, err = ht.GetHealth("openai-fail")
	if err != nil {
		t.Fatalf("GetHealth error: %v", err)
	}
	if h.Status != "unhealthy" {
		t.Errorf("expected status unhealthy, got %q", h.Status)
	}
	if h.ConsecutiveFailures != 2 {
		t.Errorf("expected consecutiveFailures 2, got %d", h.ConsecutiveFailures)
	}
	if h.CooldownUntil <= time.Now().Unix() {
		t.Errorf("expected cooldown in future, got %d", h.CooldownUntil)
	}
	if h.LastFailureReason != "server_error" {
		t.Errorf("expected failure reason server_error, got %s", h.LastFailureReason)
	}
}

func TestCircuitBreakerQuota(t *testing.T) {
	ht, cleanup := setupTestHealth(t)
	defer cleanup()

	// Record a quota failure (status 402)
	err := ht.RecordFailure("openai", 402, "insufficient quota")
	if err != nil {
		t.Fatalf("RecordFailure error: %v", err)
	}

	h, err := ht.GetHealth("openai")
	if err != nil {
		t.Fatalf("GetHealth error: %v", err)
	}

	if h.Status != "unhealthy" {
		t.Errorf("expected status unhealthy, got %s", h.Status)
	}
	if h.ConsecutiveFailures != 1 {
		t.Errorf("expected consecutiveFailures 1, got %d", h.ConsecutiveFailures)
	}
	if h.CooldownUntil <= time.Now().Unix() {
		t.Errorf("expected cooldown in future, got %d", h.CooldownUntil)
	}
	if h.LastFailureReason != "quota" {
		t.Errorf("expected failure reason quota, got %s", h.LastFailureReason)
	}
}

func TestRecordSuccessResets(t *testing.T) {
	ht, cleanup := setupTestHealth(t)
	defer cleanup()

	// Record a failure first
	err := ht.RecordFailure("openai", 500, "internal server error")
	if err != nil {
		t.Fatalf("RecordFailure error: %v", err)
	}

	// Then record success
	err = ht.RecordSuccess("openai")
	if err != nil {
		t.Fatalf("RecordSuccess error: %v", err)
	}

	h, err := ht.GetHealth("openai")
	if err != nil {
		t.Fatalf("GetHealth error: %v", err)
	}

	if h.Status != "healthy" {
		t.Errorf("expected status healthy, got %s", h.Status)
	}
	if h.ConsecutiveFailures != 0 {
		t.Errorf("expected consecutiveFailures 0, got %d", h.ConsecutiveFailures)
	}
	if h.CooldownUntil != 0 {
		t.Errorf("expected cooldownUntil 0, got %d", h.CooldownUntil)
	}
	if h.SuccessCount != 1 {
		t.Errorf("expected successCount 1, got %d", h.SuccessCount)
	}
	if h.TotalRequests != 2 {
		t.Errorf("expected totalRequests 2, got %d", h.TotalRequests)
	}
	if h.LastFailureReason != "" {
		t.Errorf("expected LastFailureReason cleared, got %q", h.LastFailureReason)
	}
}

func TestRecordFailureSkipsClientDisconnect(t *testing.T) {
	ht, cleanup := setupTestHealth(t)
	defer cleanup()

	// Client-side disconnects (context canceled) should not count as provider failures.
	err := ht.RecordFailure("openai", 0, "Post \"...\": context canceled")
	if err != nil {
		t.Fatalf("RecordFailure error: %v", err)
	}

	h, err := ht.GetHealth("openai")
	if err != nil {
		t.Fatalf("GetHealth error: %v", err)
	}

	if h.Status != "" {
		t.Errorf("expected no status change for client disconnect, got %q", h.Status)
	}
	if h.ConsecutiveFailures != 0 {
		t.Errorf("expected consecutiveFailures 0, got %d", h.ConsecutiveFailures)
	}
	if h.TotalRequests != 0 {
		t.Errorf("expected totalRequests 0, got %d", h.TotalRequests)
	}
}

func TestInvalidRequestThresholdIsHigher(t *testing.T) {
	ht, cleanup := setupTestHealth(t)
	defer cleanup()

	// invalid_request threshold is 50 — record 49 times, should still be healthy.
	for i := 0; i < 49; i++ {
		err := ht.RecordFailure("openai", 400, "invalid params")
		if err != nil {
			t.Fatalf("RecordFailure error: %v", err)
		}
	}

	h, err := ht.GetHealth("openai")
	if err != nil {
		t.Fatalf("GetHealth error: %v", err)
	}

	if h.Status != "" {
		t.Errorf("expected status empty after 49 invalid_requests (threshold=50), got %q", h.Status)
	}
	if h.ConsecutiveFailures != 49 {
		t.Errorf("expected consecutiveFailures 49, got %d", h.ConsecutiveFailures)
	}

	// 50th failure should trip the breaker.
	err = ht.RecordFailure("openai", 400, "invalid params")
	if err != nil {
		t.Fatalf("RecordFailure error: %v", err)
	}

	h, err = ht.GetHealth("openai")
	if err != nil {
		t.Fatalf("GetHealth error: %v", err)
	}

	if h.Status != "unhealthy" {
		t.Errorf("expected status unhealthy after 50 invalid_requests, got %q", h.Status)
	}
}
