package health

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"smart-router/internal/types"
)

var circuitRules = map[string]struct {
	threshold int
	cooldown  time.Duration
}{
	"auth":        {threshold: 1, cooldown: 1 * time.Hour},
	"quota":       {threshold: 1, cooldown: 5 * time.Hour},
	"rate_limit":  {threshold: 3, cooldown: 5 * time.Minute},
	"server_error": {threshold: 2, cooldown: 2 * time.Minute},
	"connection":  {threshold: 2, cooldown: 1 * time.Minute},
	"timeout":     {threshold: 2, cooldown: 2 * time.Minute},
	"unknown":     {threshold: 3, cooldown: 1 * time.Minute},
}

type HealthTracker struct {
	db *badger.DB
}

func New(path string) (*HealthTracker, error) {
	opts := badger.DefaultOptions(path).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	return &HealthTracker{db: db}, nil
}

func (h *HealthTracker) Close() error {
	return h.db.Close()
}

func key(provider string) []byte {
	return []byte("health:" + provider)
}

func (h *HealthTracker) GetHealth(provider string) (types.ProviderHealth, error) {
	var health types.ProviderHealth
	err := h.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key(provider))
		if err == badger.ErrKeyNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &health)
		})
	})
	return health, err
}

func (h *HealthTracker) RecordFailure(provider string, status int, message string) error {
	now := time.Now().Unix()
	reason := classifyFailure(status, message)

	return h.db.Update(func(txn *badger.Txn) error {
		k := key(provider)
		var health types.ProviderHealth
		item, err := txn.Get(k)
		if err == nil {
			err = item.Value(func(val []byte) error {
				return json.Unmarshal(val, &health)
			})
			if err != nil {
				return err
			}
		} else if err != badger.ErrKeyNotFound {
			return err
		}

		health.ConsecutiveFailures++
		health.LastFailureAt = now
		health.LastActivityAt = now
		health.LastFailureReason = reason
		health.TotalRequests++

		rule, ok := circuitRules[reason]
		if !ok {
			rule = circuitRules["unknown"]
		}

		if health.ConsecutiveFailures >= rule.threshold {
			health.Status = "unhealthy"
			health.CooldownUntil = now + int64(rule.cooldown.Seconds())
		}

		data, err := json.Marshal(health)
		if err != nil {
			return err
		}
		return txn.Set(k, data)
	})
}

func (h *HealthTracker) RecordSuccess(provider string) error {
	now := time.Now().Unix()

	return h.db.Update(func(txn *badger.Txn) error {
		k := key(provider)
		var health types.ProviderHealth
		item, err := txn.Get(k)
		if err == nil {
			err = item.Value(func(val []byte) error {
				return json.Unmarshal(val, &health)
			})
			if err != nil {
				return err
			}
		} else if err != badger.ErrKeyNotFound {
			return err
		}

		health.Status = "healthy"
		health.ConsecutiveFailures = 0
		health.CooldownUntil = 0
		health.LastSuccessAt = now
		health.LastActivityAt = now
		health.SuccessCount++
		health.TotalRequests++

		data, err := json.Marshal(health)
		if err != nil {
			return err
		}
		return txn.Set(k, data)
	})
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
