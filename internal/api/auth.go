package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"smart-router/internal/auth"
	"smart-router/internal/db"
	"smart-router/internal/types"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey int

const clientKeyContextKey contextKey = iota

// Auth handles API key validation, rate limiting, and quota enforcement.
type Auth struct {
	db          *db.DB
	rl          *auth.RateLimiter
	keyCache    map[string]cachedKey
	keyCacheMu  sync.RWMutex
	cacheTTL    time.Duration
}

type cachedKey struct {
	key      *types.APIKey
	loadedAt time.Time
}

// NewAuth creates a new Auth handler with an in-memory key cache.
func NewAuth(database *db.DB, rateLimiter *auth.RateLimiter) *Auth {
	a := &Auth{
		db:       database,
		rl:       rateLimiter,
		keyCache: make(map[string]cachedKey),
		cacheTTL: 30 * time.Second,
	}
	go a.cacheEvictionWorker()
	return a
}

// Middleware returns an HTTP middleware that validates API keys.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for OPTIONS requests (CORS preflight)
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// Skip auth for admin endpoints (they use X-Admin-Key)
		if strings.HasPrefix(r.URL.Path, "/v1/plans") ||
			strings.HasPrefix(r.URL.Path, "/v1/keys") ||
			strings.HasPrefix(r.URL.Path, "/v1/audit") ||
			r.URL.Path == "/v1/stats" ||
			r.URL.Path == "/v1/stats/aggregated" {
			next.ServeHTTP(w, r)
			return
		}

		token := auth.ParseBearerToken(r.Header.Get("Authorization"))
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

		// Extract requested plan from header or body (same logic as handler)
		planSlug := r.Header.Get("X-Plan")
		if planSlug == "" {
			planSlug = "default"
		}

		// Plan access check
		if len(apiKey.Plans) > 0 {
			allowed := false
			for _, p := range apiKey.Plans {
				if p == planSlug {
					allowed = true
					break
				}
			}
			if !allowed {
				writeError(w, http.StatusForbidden, "plan not allowed for this key")
				return
			}
		}

		// Rate limit check
		if ok, reason := a.rl.Allow(token, apiKey.RateLimitRPM, apiKey.RateLimitRPD); !ok {
			writeError(w, http.StatusTooManyRequests, fmt.Sprintf("rate limit exceeded: %s", reason))
			return
		}

		// Monthly quota checks
		now := time.Now()
		if apiKey.MonthlyTokenLimit > 0 || apiKey.MonthlyRequestLimit > 0 {
			usage, err := a.db.GetKeyMonthlyUsage(token, now.Year(), int(now.Month()))
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
		}

		// Update last_used_at async
		go a.db.UpdateKeyLastUsed(token)

		ctx := context.WithValue(r.Context(), clientKeyContextKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ClientKeyFromContext extracts the API key from the request context.
func ClientKeyFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(clientKeyContextKey).(string); ok {
		return v
	}
	return ""
}

func (a *Auth) getKeyCached(key string) (*types.APIKey, error) {
	a.keyCacheMu.RLock()
	entry, ok := a.keyCache[key]
	a.keyCacheMu.RUnlock()
	if ok && time.Since(entry.loadedAt) < a.cacheTTL {
		return entry.key, nil
	}

	k, err := a.db.GetAPIKey(key)
	if err != nil {
		return nil, err
	}

	a.keyCacheMu.Lock()
	a.keyCache[key] = cachedKey{key: k, loadedAt: time.Now()}
	a.keyCacheMu.Unlock()
	return k, nil
}

func (a *Auth) cacheEvictionWorker() {
	ticker := time.NewTicker(a.cacheTTL)
	defer ticker.Stop()
	for range ticker.C {
		a.keyCacheMu.Lock()
		for k, v := range a.keyCache {
			if time.Since(v.loadedAt) > a.cacheTTL {
				delete(a.keyCache, k)
			}
		}
		a.keyCacheMu.Unlock()
	}
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
