package auth

import (
	"sync"
	"time"
)

// RateLimiter implements a per-key sliding-window rate limiter.
type RateLimiter struct {
	windows map[string]*window
	mu      sync.RWMutex
}

type window struct {
	rpm []int64
	rpd []int64
	mu  sync.Mutex
}

// NewRateLimiter creates a new rate limiter with a background cleanup goroutine.
func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{
		windows: make(map[string]*window),
	}
	go rl.cleanup()
	return rl
}

// Allow checks if the key is within its rate limits.
// Returns (true, "") if allowed, or (false, reason) if blocked.
func (rl *RateLimiter) Allow(key string, rpmLimit, rpdLimit int) (bool, string) {
	if rpmLimit <= 0 && rpdLimit <= 0 {
		return true, ""
	}

	rl.mu.RLock()
	w, ok := rl.windows[key]
	rl.mu.RUnlock()
	if !ok {
		rl.mu.Lock()
		if w2, ok2 := rl.windows[key]; ok2 {
			w = w2
		} else {
			w = &window{}
			rl.windows[key] = w
		}
		rl.mu.Unlock()
	}

	now := time.Now().Unix()

	w.mu.Lock()
	defer w.mu.Unlock()

	// Purge old entries
	cutoffMinute := now - 60
	cutoffDay := now - 86400

	var rpmActive []int64
	for _, ts := range w.rpm {
		if ts > cutoffMinute {
			rpmActive = append(rpmActive, ts)
		}
	}
	w.rpm = rpmActive

	var rpdActive []int64
	for _, ts := range w.rpd {
		if ts > cutoffDay {
			rpdActive = append(rpdActive, ts)
		}
	}
	w.rpd = rpdActive

	// Check limits
	if rpmLimit > 0 && len(w.rpm) >= rpmLimit {
		return false, "rate_limit_rpm"
	}
	if rpdLimit > 0 && len(w.rpd) >= rpdLimit {
		return false, "rate_limit_rpd"
	}

	// Record this request
	w.rpm = append(w.rpm, now)
	w.rpd = append(w.rpd, now)
	return true, ""
}

// cleanup removes stale windows every minute to prevent unbounded growth.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Unix() - 86400
		rl.mu.Lock()
		for k, w := range rl.windows {
			w.mu.Lock()
			var activeRPD []int64
			for _, ts := range w.rpd {
				if ts > cutoff {
					activeRPD = append(activeRPD, ts)
				}
			}
			w.rpd = activeRPD

			cutoffRPM := time.Now().Unix() - 60
			var activeRPM []int64
			for _, ts := range w.rpm {
				if ts > cutoffRPM {
					activeRPM = append(activeRPM, ts)
				}
			}
			w.rpm = activeRPM

			// Delete windows with no active entries to prevent unbounded growth.
			if len(w.rpm) == 0 && len(w.rpd) == 0 {
				delete(rl.windows, k)
			}
			w.mu.Unlock()
		}
		rl.mu.Unlock()
	}
}
