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
	"auth":         {threshold: 1, cooldown: 1 * time.Hour},
	"quota":        {threshold: 1, cooldown: 5 * time.Hour},
	"rate_limit":   {threshold: 3, cooldown: 5 * time.Minute},
	"server_error": {threshold: 2, cooldown: 2 * time.Minute},
	"connection":   {threshold: 2, cooldown: 1 * time.Minute},
	"timeout":      {threshold: 2, cooldown: 2 * time.Minute},
	"unknown":      {threshold: 3, cooldown: 1 * time.Minute},
}

type HealthTracker struct {
	db    *badger.DB
	cache map[string]types.ProviderHealth
	dirty map[string]bool
	mu    sync.RWMutex
	stop  chan struct{}
	wg    sync.WaitGroup
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
	return h, nil
}

func (h *HealthTracker) Close() error {
	close(h.stop)
	h.wg.Wait()
	h.flush()
	return h.db.Close()
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

	// Warm cache
	h.mu.Lock()
	h.cache[provider] = result
	h.mu.Unlock()
	return result, nil
}

func (h *HealthTracker) RecordFailure(provider string, status int, message string) error {
	now := time.Now().Unix()
	reason := classifyFailure(status, message)
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

func (h *HealthTracker) RecordSuccess(provider string) error {
	now := time.Now().Unix()

	h.mu.Lock()
	health := h.cache[provider]
	health.Status = "healthy"
	health.ConsecutiveFailures = 0
	health.CooldownUntil = 0
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

func (h *HealthTracker) flush() {
	h.mu.Lock()
	toFlush := make(map[string]types.ProviderHealth, len(h.dirty))
	for name := range h.dirty {
		toFlush[name] = h.cache[name]
	}
	h.dirty = make(map[string]bool)
	h.mu.Unlock()

	if len(toFlush) == 0 {
		return
	}

	if err := h.db.Update(func(txn *badger.Txn) error {
		for name, health := range toFlush {
			data, err := json.Marshal(health)
			if err != nil {
				log.Printf("[HEALTH] marshal failed for %s: %v", name, err)
				continue
			}
			if err := txn.Set(key(name), data); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		log.Printf("[HEALTH] flush failed: %v", err)
	}
}

func classifyFailure(status int, message string) string {
	msg := strings.ToLower(message)
	if status == 401 || strings.Contains(msg, "authentication") || strings.Contains(msg, "unauthorized") {
		return "auth"
	}
	if status == 402 || strings.Contains(msg, "quota") || strings.Contains(msg, "credit") || strings.Contains(msg, "billing") {
		return "quota"
	}
	if status == 429 || strings.Contains(msg, "rate limit") {
		return "rate_limit"
	}
	if status >= 500 && status < 600 {
		return "server_error"
	}
	if strings.Contains(msg, "connection") || strings.Contains(msg, "refused") {
		return "connection"
	}
	if strings.Contains(msg, "timeout") {
		return "timeout"
	}
	return "unknown"
}
