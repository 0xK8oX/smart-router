package health

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
	"smart-router/internal/types"
)

var circuitRules = map[string]struct {
	threshold int
	cooldown  time.Duration
}{
	"auth":             {threshold: 1, cooldown: 1 * time.Hour},
	"quota":            {threshold: 1, cooldown: 5 * time.Hour},
	"rate_limit":       {threshold: 3, cooldown: 5 * time.Minute},
	"server_error":     {threshold: 2, cooldown: 2 * time.Minute},
	"connection":       {threshold: 2, cooldown: 1 * time.Minute},
	"timeout":          {threshold: 2, cooldown: 2 * time.Minute},
	"invalid_request":  {threshold: 50, cooldown: 5 * time.Minute},
	"unknown":          {threshold: 3, cooldown: 1 * time.Minute},
}

type HealthTracker struct {
	db      *badger.DB
	cache   map[string]types.ProviderHealth
	dirty   map[string]bool
	mu      sync.RWMutex
	stop    chan struct{}
	wg      sync.WaitGroup
	closing bool
}

func New(path string) (*HealthTracker, error) {
	opts := badger.DefaultOptions(path).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}

	h := &HealthTracker{
		db:    db,
		cache: make(map[string]types.ProviderHealth),
		dirty: make(map[string]bool),
		stop:  make(chan struct{}),
	}
	h.startFlushWorker()
	h.startGCWorker()
	return h, nil
}

func (h *HealthTracker) Close() error {
	h.mu.Lock()
	h.closing = true
	h.mu.Unlock()
	close(h.stop)
	h.wg.Wait()
	flushErr := h.flush()
	if err := h.db.Close(); err != nil {
		return err
	}
	return flushErr
}

func key(provider string) []byte {
	return []byte("health:" + provider)
}

func (h *HealthTracker) GetHealth(provider string) (types.ProviderHealth, error) {
	h.mu.RLock()
	health, ok := h.cache[provider]
	h.mu.RUnlock()
	if ok {
		return health, nil
	}

	var result types.ProviderHealth
	err := h.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key(provider))
		if err == badger.ErrKeyNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &result)
		})
	})
	if err != nil {
		return types.ProviderHealth{}, err
	}

	// Warm cache with double-check to avoid redundant writes.
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.cache[provider]; ok {
		return existing, nil
	}
	h.cache[provider] = result
	return result, nil
}

func (h *HealthTracker) RecordFailure(provider string, status int, message string) error {
	h.mu.RLock()
	if h.closing {
		h.mu.RUnlock()
		return nil
	}
	h.mu.RUnlock()

	now := time.Now().Unix()
	reason := ClassifyFailure(status, message)
	// Client-side disconnects (e.g. context canceled) should not count as provider failures.
	if reason == "" {
		return nil
	}
	rule := circuitRules[reason]
	if rule.threshold == 0 {
		rule = circuitRules["unknown"]
	}

	h.mu.Lock()
	health := h.cache[provider]
	health.ConsecutiveFailures++
	health.LastFailureAt = now
	health.LastActivityAt = now
	health.LastFailureReason = reason
	health.TotalRequests++

	if health.ConsecutiveFailures >= rule.threshold {
		health.Status = "unhealthy"
		health.CooldownUntil = now + int64(rule.cooldown.Seconds())
	}

	h.cache[provider] = health
	h.dirty[provider] = true
	h.mu.Unlock()
	return nil
}

// ResetProvider clears the unhealthy state for a provider, setting it back
// to healthy without requiring a successful HTTP call.
func (h *HealthTracker) ResetProvider(provider string) error {
	h.mu.RLock()
	if h.closing {
		h.mu.RUnlock()
		return nil
	}
	h.mu.RUnlock()

	now := time.Now().Unix()

	h.mu.Lock()
	health := h.cache[provider]
	health.Status = "healthy"
	health.ConsecutiveFailures = 0
	health.CooldownUntil = 0
	health.LastFailureReason = ""
	health.LastActivityAt = now

	h.cache[provider] = health
	h.dirty[provider] = true
	h.mu.Unlock()
	return nil
}

func (h *HealthTracker) RecordSuccess(provider string) error {
	h.mu.RLock()
	if h.closing {
		h.mu.RUnlock()
		return nil
	}
	h.mu.RUnlock()

	now := time.Now().Unix()

	h.mu.Lock()
	health := h.cache[provider]
	health.Status = "healthy"
	health.ConsecutiveFailures = 0
	health.CooldownUntil = 0
	health.LastFailureReason = ""
	health.LastSuccessAt = now
	health.LastActivityAt = now
	health.SuccessCount++
	health.TotalRequests++

	h.cache[provider] = health
	h.dirty[provider] = true
	h.mu.Unlock()
	return nil
}

func (h *HealthTracker) startFlushWorker() {
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				h.flush()
				// If flush was slow, discard any accumulated tick
				// so we don't flush back-to-back.
				select {
				case <-ticker.C:
				default:
				}
			case <-h.stop:
				return
			}
		}
	}()
}

func (h *HealthTracker) startGCWorker() {
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				for {
					err := h.db.RunValueLogGC(0.5)
					if err == badger.ErrNoRewrite {
						break
					}
					if err != nil {
						log.Printf("[HEALTH] value log GC error: %v", err)
						break
					}
				}
			case <-h.stop:
				return
			}
		}
	}()
}

func (h *HealthTracker) flush() error {
	h.mu.Lock()
	toFlush := make(map[string]types.ProviderHealth, len(h.dirty))
	for name := range h.dirty {
		toFlush[name] = h.cache[name]
	}
	h.mu.Unlock()

	if len(toFlush) == 0 {
		return nil
	}

	marshalFailed := make(map[string]bool)
	if err := h.db.Update(func(txn *badger.Txn) error {
		for name, health := range toFlush {
			data, err := json.Marshal(health)
			if err != nil {
				log.Printf("[HEALTH] marshal failed for %s: %v", name, err)
				marshalFailed[name] = true
				continue
			}
			if err := txn.Set(key(name), data); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		log.Printf("[HEALTH] flush failed: %v", err)
		// Restore dirty markers so un-flushed entries are retried.
		h.mu.Lock()
		for name := range toFlush {
			h.dirty[name] = true
		}
		h.mu.Unlock()
		return err
	}

	// Only clear dirty after successful flush; preserve dirty for marshal failures
	// and for entries that were modified during the flush window.
	h.mu.Lock()
	for name := range toFlush {
		if !marshalFailed[name] && h.cache[name] == toFlush[name] {
			delete(h.dirty, name)
		}
	}
	h.mu.Unlock()
	return nil
}

// List returns all provider health records from the DB.
func (h *HealthTracker) List() (map[string]types.ProviderHealth, error) {
	result := make(map[string]types.ProviderHealth)

	h.mu.RLock()
	for name, health := range h.cache {
		result[name] = health
	}
	h.mu.RUnlock()

	err := h.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchSize = 10
		it := txn.NewIterator(opts)
		defer it.Close()

		prefix := []byte("health:")
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			name := strings.TrimPrefix(string(item.Key()), "health:")
			if _, ok := result[name]; ok {
				continue // prefer cached version
			}
			var health types.ProviderHealth
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &health)
			}); err != nil {
				log.Printf("[HEALTH] unmarshal failed for %s: %v", name, err)
				continue
			}
			result[name] = health
		}
		return nil
	})
	return result, err
}

func ClassifyFailure(status int, message string) string {
	msg := strings.ToLower(message)
	// Client-side disconnects should not count as provider failures.
	if strings.Contains(msg, "context canceled") {
		return ""
	}
	if status == 401 || strings.Contains(msg, "authentication") || strings.Contains(msg, "unauthorized") {
		return "auth"
	}
	if status == 402 || strings.Contains(msg, "quota") || strings.Contains(msg, "credit") || strings.Contains(msg, "billing") {
		return "quota"
	}
	if status == 429 || strings.Contains(msg, "rate limit") {
		return "rate_limit"
	}
	if status == 400 || status == 422 {
		return "invalid_request"
	}
	if status >= 500 && status < 600 {
		return "server_error"
	}
	if strings.Contains(msg, "connection") || strings.Contains(msg, "refused") {
		return "connection"
	}
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "context deadline exceeded") {
		return "timeout"
	}
	return "unknown"
}
