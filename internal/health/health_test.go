package health

import (
	"os"
	"testing"
	"time"
)

func TestCircuitBreakerQuota(t *testing.T) {
	dir, err := os.MkdirTemp("", "health-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ht, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ht.Close()

	// Record a quota failure (status 402)
	err = ht.RecordFailure("openai", 402, "insufficient quota")
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
	dir, err := os.MkdirTemp("", "health-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ht, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ht.Close()

	// Record a failure first
	err = ht.RecordFailure("openai", 500, "internal server error")
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
}
