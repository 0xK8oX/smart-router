package auth

import (
	"testing"
	"time"
)

func TestRateLimiterAllow(t *testing.T) {
	rl := NewRateLimiter()

	// No limits set — always allowed
	ok, reason := rl.Allow("key1", 0, 0)
	if !ok {
		t.Errorf("expected allowed with no limits, got reason=%s", reason)
	}

	// RPM limit of 2
	ok, _ = rl.Allow("key2", 2, 0)
	if !ok {
		t.Error("expected first request allowed")
	}
	ok, _ = rl.Allow("key2", 2, 0)
	if !ok {
		t.Error("expected second request allowed")
	}
	ok, reason = rl.Allow("key2", 2, 0)
	if ok {
		t.Error("expected third request blocked")
	}
	if reason != "rate_limit_rpm" {
		t.Errorf("expected rate_limit_rpm, got %s", reason)
	}
}

func TestRateLimiterWindowExpires(t *testing.T) {
	rl := NewRateLimiter()

	// RPM limit of 1
	ok, _ := rl.Allow("key3", 1, 0)
	if !ok {
		t.Error("expected first request allowed")
	}

	// Manually backdate the stored timestamp so it appears old
	rl.mu.RLock()
	w := rl.windows["key3"]
	rl.mu.RUnlock()
	w.mu.Lock()
	w.rpm[0] = time.Now().Unix() - 61
	w.mu.Unlock()

	ok, _ = rl.Allow("key3", 1, 0)
	if !ok {
		t.Error("expected request allowed after window expiry")
	}
}
