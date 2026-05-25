package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"smart-router/internal/alerts"
	"smart-router/internal/auth"
	"smart-router/internal/db"
	"smart-router/internal/types"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey int

const (
	clientKeyContextKey contextKey = iota
	apiKeyContextKey
	planSlugContextKey
	bodyContextKey
)

// resolvePlanFromBody extracts the plan slug from the request body model field.
// Supports auto-<plan> prefix and plan/model syntax.
func resolvePlanFromBody(body map[string]interface{}) string {
	planSlug := "default"
	if model, ok := body["model"].(string); ok {
		if strings.HasPrefix(model, "auto-") {
			planSlug = strings.TrimPrefix(model, "auto-")
		} else if idx := strings.Index(model, "/"); idx > 0 {
			planSlug = model[:idx]
		}
	}
	if planSlug == "" {
		planSlug = "default"
	}
	return planSlug
}

// Auth handles API key validation, rate limiting, and quota enforcement.
type Auth struct {
	db            *db.DB
	rl            *auth.RateLimiter
	keyCache      map[string]cachedKey
	keyCacheMu    sync.RWMutex
	keyCacheTTL   time.Duration
	usageCache    map[string]usageCacheEntry
	usageCacheMu  sync.RWMutex
	usageCacheTTL time.Duration
	groupCache    map[int64]groupCacheEntry
	groupCacheMu  sync.RWMutex
	groupCacheTTL time.Duration
	lastUsedBatch map[string]struct{}
	lastUsedMu    sync.Mutex
	alertedKeys   map[string]bool // key="key:YYYY-MM" tracks 80% quota alerts sent this month
	alertedMu     sync.Mutex
	stop          chan struct{}
	closeOnce     sync.Once
	wg            sync.WaitGroup
}

type cachedKey struct {
	key      *types.APIKey
	loadedAt time.Time
}

type usageCacheEntry struct {
	usage    *db.MonthlyUsage
	loadedAt time.Time
}

type groupCacheEntry struct {
	group    *types.KeyGroup
	loadedAt time.Time
}

// NewAuth creates a new Auth handler with an in-memory key cache.
func NewAuth(database *db.DB, rateLimiter *auth.RateLimiter) *Auth {
	a := &Auth{
		db:            database,
		rl:            rateLimiter,
		keyCache:      make(map[string]cachedKey),
		keyCacheTTL:   30 * time.Second,
		usageCache:    make(map[string]usageCacheEntry),
		usageCacheTTL: 10 * time.Second,
		groupCache:    make(map[int64]groupCacheEntry),
		groupCacheTTL: 60 * time.Second,
		lastUsedBatch: make(map[string]struct{}),
		alertedKeys:   make(map[string]bool),
		stop:          make(chan struct{}),
	}
	a.wg.Add(1)
	go a.cacheEvictionWorker()
	a.wg.Add(1)
	go a.lastUsedFlushWorker()
	return a
}

// Close stops the background workers and waits for them to finish.
func (a *Auth) Close() {
	a.closeOnce.Do(func() {
		close(a.stop)
		a.wg.Wait()
	})
}

// Middleware returns an HTTP middleware that validates API keys.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for OPTIONS requests (CORS preflight)
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// Skip auth for public and admin endpoints
		if strings.HasPrefix(r.URL.Path, "/v1/models") ||
			strings.HasPrefix(r.URL.Path, "/v1/health") ||
			strings.HasPrefix(r.URL.Path, "/v1/plans") ||
			strings.HasPrefix(r.URL.Path, "/v1/keys") ||
			strings.HasPrefix(r.URL.Path, "/v1/audit") ||
			strings.HasPrefix(r.URL.Path, "/v1/groups") ||
			r.URL.Path == "/v1/stats" ||
			r.URL.Path == "/v1/stats/aggregated" {
			next.ServeHTTP(w, r)
			return
		}

		token := auth.ParseBearerToken(r.Header.Get("Authorization"))
		if token == "" {
			// Anthropic clients send x-api-key instead of Authorization: Bearer
			token = r.Header.Get("x-api-key")
		}
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing Authorization header")
			return
		}

		apiKey, err := a.getKeyCached(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid api key")
			return
		}

		if apiKey.Disabled {
			writeError(w, http.StatusUnauthorized, "api key disabled")
			return
		}

		if apiKey.ExpiresAt != nil && *apiKey.ExpiresAt < time.Now().Unix() {
			alerts.SendWebhookExpiredAlert(apiKey.WebhookURL, apiKey.Name)
			writeError(w, http.StatusUnauthorized, "api key expired")
			return
		}

		// IP allowlist check
		if len(apiKey.AllowedIPs) > 0 {
			clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
			if clientIP == "" {
				clientIP = r.RemoteAddr
			}
			if !isIPAllowed(clientIP, apiKey.AllowedIPs) {
				writeError(w, http.StatusForbidden, "client ip not allowed")
				return
			}
		}

		// Rate limit check — before body read so rejected requests pay no I/O cost
		if ok, reason := a.rl.Allow(token, apiKey.RateLimitRPM, apiKey.RateLimitRPD); !ok {
			writeError(w, http.StatusTooManyRequests, fmt.Sprintf("rate limit exceeded: %s", reason))
			return
		}

		// Monthly quota checks — before body read
		now := time.Now().UTC()
		if apiKey.MonthlyTokenLimit > 0 || apiKey.MonthlyRequestLimit > 0 {
			usage, err := a.getUsageCached(token, now.Year(), int(now.Month()))
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to check quota")
				return
			}
			if apiKey.MonthlyTokenLimit > 0 && usage.RequestTokens+usage.ResponseTokens >= int64(apiKey.MonthlyTokenLimit) {
				writeError(w, http.StatusTooManyRequests, "monthly token quota exceeded")
				return
			}
			if apiKey.MonthlyRequestLimit > 0 && usage.RequestCount >= int64(apiKey.MonthlyRequestLimit) {
				writeError(w, http.StatusTooManyRequests, "monthly request quota exceeded")
				return
			}

			// Check 80% quota threshold and trigger webhook
			a.checkAndSendQuotaAlert(apiKey, usage)
		}

		// Group quota checks — before body read
		if apiKey.GroupID != nil {
			group, err := a.getGroupCached(*apiKey.GroupID)
			if err == nil && (group.MonthlyTokenLimit > 0 || group.MonthlyRequestLimit > 0) {
				groupUsage, err := a.getGroupUsageCached(*apiKey.GroupID, now.UTC().Year(), int(now.UTC().Month()))
				if err != nil {
					writeError(w, http.StatusInternalServerError, "failed to check group quota")
					return
				}
				if group.MonthlyTokenLimit > 0 && groupUsage.RequestTokens+groupUsage.ResponseTokens >= int64(group.MonthlyTokenLimit) {
					writeError(w, http.StatusTooManyRequests, "group monthly token quota exceeded")
					return
				}
				if group.MonthlyRequestLimit > 0 && groupUsage.RequestCount >= int64(group.MonthlyRequestLimit) {
					writeError(w, http.StatusTooManyRequests, "group monthly request quota exceeded")
					return
				}
			}
		}

		// Batch last_used_at update
		a.markLastUsed(token)

		// Extract requested plan from body (supports auto-<plan> and plan/model syntax)
		lr := io.LimitReader(r.Body, maxRequestBodySize+1)
		bodyBytes, err := io.ReadAll(lr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "failed to read body")
			return
		}
		if int64(len(bodyBytes)) > maxRequestBodySize {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		var body map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		planSlug := resolvePlanFromBody(body)

		ctx := context.WithValue(r.Context(), bodyContextKey, body)
		r = r.WithContext(ctx)

		// Plan access check
		if len(apiKey.Plans) > 0 {
			allowed := false
			for _, p := range apiKey.Plans {
				if p == planSlug || p == "*" {
					allowed = true
					break
				}
			}
			if !allowed {
				writeError(w, http.StatusForbidden, "plan not allowed for this key")
				return
			}
		}

		ctx = context.WithValue(r.Context(), clientKeyContextKey, token)
		ctx = context.WithValue(ctx, apiKeyContextKey, apiKey)
		ctx = context.WithValue(ctx, planSlugContextKey, planSlug)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ClientKeyFromContext extracts the API key string from the request context.
func ClientKeyFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(clientKeyContextKey).(string); ok {
		return v
	}
	return ""
}

// APIKeyFromContext extracts the validated APIKey from the request context.
func APIKeyFromContext(ctx context.Context) *types.APIKey {
	if v, ok := ctx.Value(apiKeyContextKey).(*types.APIKey); ok {
		return v
	}
	return nil
}

// PlanSlugFromContext extracts the resolved plan slug from the request context.
func PlanSlugFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(planSlugContextKey).(string); ok {
		return v
	}
	return ""
}

// BodyFromContext extracts the parsed request body from the request context.
func BodyFromContext(ctx context.Context) (map[string]interface{}, bool) {
	if v, ok := ctx.Value(bodyContextKey).(map[string]interface{}); ok {
		return v, true
	}
	return nil, false
}

func (a *Auth) getKeyCached(key string) (*types.APIKey, error) {
	a.keyCacheMu.RLock()
	entry, ok := a.keyCache[key]
	a.keyCacheMu.RUnlock()
	if ok && time.Since(entry.loadedAt) < a.keyCacheTTL {
		return entry.key, nil
	}

	k, err := a.db.GetAPIKey(key)
	if err != nil {
		return nil, err
	}

	a.keyCacheMu.Lock()
	defer a.keyCacheMu.Unlock()
	if entry, ok := a.keyCache[key]; ok && time.Since(entry.loadedAt) < a.keyCacheTTL {
		return entry.key, nil
	}
	a.keyCache[key] = cachedKey{key: k, loadedAt: time.Now()}
	return k, nil
}

func (a *Auth) getUsageCached(key string, year, month int) (*db.MonthlyUsage, error) {
	cacheKey := fmt.Sprintf("%s:%d-%02d", key, year, month)
	a.usageCacheMu.RLock()
	entry, ok := a.usageCache[cacheKey]
	a.usageCacheMu.RUnlock()
	if ok && time.Since(entry.loadedAt) < a.usageCacheTTL {
		return entry.usage, nil
	}

	usage, err := a.db.GetKeyMonthlyUsage(key, year, month)
	if err != nil {
		return nil, err
	}

	a.usageCacheMu.Lock()
	defer a.usageCacheMu.Unlock()
	if entry, ok := a.usageCache[cacheKey]; ok && time.Since(entry.loadedAt) < a.usageCacheTTL {
		return entry.usage, nil
	}
	a.usageCache[cacheKey] = usageCacheEntry{usage: usage, loadedAt: time.Now()}
	return usage, nil
}

func (a *Auth) getGroupCached(id int64) (*types.KeyGroup, error) {
	a.groupCacheMu.RLock()
	entry, ok := a.groupCache[id]
	a.groupCacheMu.RUnlock()
	if ok && time.Since(entry.loadedAt) < a.groupCacheTTL {
		return entry.group, nil
	}

	group, err := a.db.GetKeyGroup(id)
	if err != nil {
		return nil, err
	}

	a.groupCacheMu.Lock()
	defer a.groupCacheMu.Unlock()
	if entry, ok := a.groupCache[id]; ok && time.Since(entry.loadedAt) < a.groupCacheTTL {
		return entry.group, nil
	}
	a.groupCache[id] = groupCacheEntry{group: group, loadedAt: time.Now()}
	return group, nil
}

func (a *Auth) getGroupUsageCached(groupID int64, year, month int) (*db.MonthlyUsage, error) {
	cacheKey := fmt.Sprintf("group:%d:%d-%02d", groupID, year, month)
	a.usageCacheMu.RLock()
	entry, ok := a.usageCache[cacheKey]
	a.usageCacheMu.RUnlock()
	if ok && time.Since(entry.loadedAt) < a.usageCacheTTL {
		return entry.usage, nil
	}

	usage, err := a.db.GetGroupMonthlyUsage(groupID, year, month)
	if err != nil {
		return nil, err
	}

	a.usageCacheMu.Lock()
	defer a.usageCacheMu.Unlock()
	if entry, ok := a.usageCache[cacheKey]; ok && time.Since(entry.loadedAt) < a.usageCacheTTL {
		return entry.usage, nil
	}
	a.usageCache[cacheKey] = usageCacheEntry{usage: usage, loadedAt: time.Now()}
	return usage, nil
}

func (a *Auth) markLastUsed(key string) {
	a.lastUsedMu.Lock()
	a.lastUsedBatch[key] = struct{}{}
	a.lastUsedMu.Unlock()
}

func (a *Auth) lastUsedFlushWorker() {
	defer a.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.lastUsedMu.Lock()
			batch := a.lastUsedBatch
			a.lastUsedBatch = make(map[string]struct{})
			a.lastUsedMu.Unlock()

			if len(batch) == 0 {
				continue
			}
			now := time.Now().Unix()
			for key := range batch {
				_ = a.db.UpdateKeyLastUsedWithTime(key, now)
			}
		case <-a.stop:
			return
		}
	}
}

func (a *Auth) cacheEvictionWorker() {
	defer a.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()

			a.keyCacheMu.Lock()
			for k, v := range a.keyCache {
				if now.Sub(v.loadedAt) > a.keyCacheTTL {
					delete(a.keyCache, k)
				}
			}
			a.keyCacheMu.Unlock()

			a.usageCacheMu.Lock()
			for k, v := range a.usageCache {
				if now.Sub(v.loadedAt) > a.usageCacheTTL {
					delete(a.usageCache, k)
				}
			}
			a.usageCacheMu.Unlock()

			a.groupCacheMu.Lock()
			for k, v := range a.groupCache {
				if now.Sub(v.loadedAt) > a.groupCacheTTL {
					delete(a.groupCache, k)
				}
			}
			a.groupCacheMu.Unlock()
		case <-a.stop:
			return
		}
	}
}

func (a *Auth) checkAndSendQuotaAlert(apiKey *types.APIKey, usage *db.MonthlyUsage) {
	if apiKey.WebhookURL == "" {
		return
	}
	now := time.Now()
	alertKey := fmt.Sprintf("%s:%d-%02d", apiKey.Key, now.UTC().Year(), now.UTC().Month())

	var percent float64
	if apiKey.MonthlyTokenLimit > 0 {
		totalTokens := usage.RequestTokens + usage.ResponseTokens
		pct := float64(totalTokens) / float64(apiKey.MonthlyTokenLimit) * 100
		if pct > percent {
			percent = pct
		}
	}
	if apiKey.MonthlyRequestLimit > 0 {
		pct := float64(usage.RequestCount) / float64(apiKey.MonthlyRequestLimit) * 100
		if pct > percent {
			percent = pct
		}
	}
	if percent < 80 {
		return
	}

	a.alertedMu.Lock()
	alreadyAlerted := a.alertedKeys[alertKey]
	if !alreadyAlerted {
		a.alertedKeys[alertKey] = true
	}
	a.alertedMu.Unlock()

	if !alreadyAlerted {
		alerts.SendWebhookQuotaAlert(apiKey.WebhookURL, apiKey.Name, percent, usage.RequestTokens, usage.ResponseTokens, usage.RequestCount)
	}
}

// InvalidateKeyCache removes a key from the in-memory caches so
// admin mutations take effect immediately.
func (a *Auth) InvalidateKeyCache(key string) {
	a.keyCacheMu.Lock()
	delete(a.keyCache, key)
	a.keyCacheMu.Unlock()

	prefix := key + ":"
	a.usageCacheMu.Lock()
	for k := range a.usageCache {
		if strings.HasPrefix(k, prefix) {
			delete(a.usageCache, k)
		}
	}
	a.usageCacheMu.Unlock()
}

// InvalidateGroupCache removes a group from the in-memory cache.
func (a *Auth) InvalidateGroupCache(groupID int64) {
	a.groupCacheMu.Lock()
	delete(a.groupCache, groupID)
	a.groupCacheMu.Unlock()

	prefix := fmt.Sprintf("group:%d:", groupID)
	a.usageCacheMu.Lock()
	for k := range a.usageCache {
		if strings.HasPrefix(k, prefix) {
			delete(a.usageCache, k)
		}
	}
	a.usageCacheMu.Unlock()
}

func isIPAllowed(ip string, allowed []string) bool {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}
	for _, cidr := range allowed {
		if cidr == ip {
			return true
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if ipNet.Contains(parsedIP) {
			return true
		}
	}
	return false
}
