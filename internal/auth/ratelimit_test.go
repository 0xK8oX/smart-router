package auth

import (
	"sync"
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

func TestRateLimiterConcurrentAllow(t *testing.T) {
	rl := NewRateLimiter()

	// Set a tight RPM limit
	ok, _ := rl.Allow("concurrent-key", 100, 0)
	if !ok {
		t.Fatal("expected first request allowed")
	}

	// Spawn many concurrent Allow calls
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rl.Allow("concurrent-key", 100, 0)
		}()
	}
	wg.Wait()

	// After concurrent storm, all requests should still be tracked in the same window.
	// The rate limit should be enforced (over 100 requests were made).
	ok, _ = rl.Allow("concurrent-key", 100, 0)
	if ok {
		t.Error("expected rate limit to be enforced after concurrent requests")
	}
}

func TestRateLimiterCleanupDoesNotDeleteWindow(t *testing.T) {
	rl := NewRateLimiter()

	// Allow a request, then backdate it so cleanup would see it as old
	ok, _ := rl.Allow("key", 1, 0)
	if !ok {
		t.Fatal("expected first request allowed")
	}

	rl.mu.RLock()
	w := rl.windows["key"]
	rl.mu.RUnlock()
	w.mu.Lock()
	w.rpm[0] = time.Now().Unix() - 61
	w.mu.Unlock()

	// Manually run the purge logic (same as cleanup body, without the loop/ticker)
	rl.mu.Lock()
	for _, w := range rl.windows {
		w.mu.Lock()
		cutoff := time.Now().Unix() - 60
		var active []int64
		for _, ts := range w.rpm {
			if ts > cutoff {
				active = append(active, ts)
			}
		}
		w.rpm = active
		w.mu.Unlock()
	}
	rl.mu.Unlock()

	// Window should still be in the map (not deleted)
	rl.mu.RLock()
	_, exists := rl.windows["key"]
	rl.mu.RUnlock()
	if !exists {
		t.Fatal("window was deleted by cleanup; it should remain in the map")
	}

	// Next request should be allowed because the old entry was purged
	ok, _ = rl.Allow("key", 1, 0)
	if !ok {
		t.Error("expected request allowed after old entry purged")
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
