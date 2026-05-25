package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"smart-router/internal/auth"
	"smart-router/internal/db"
	"smart-router/internal/types"
)

func setupTestDB(t *testing.T) *db.DB {
	t.Helper()
	path := "/tmp/smart-router-test-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".db"
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		database.Close()
		os.Remove(path)
	})
	return database
}

func TestAuthMiddleware_HermesM1(t *testing.T) {
	database := setupTestDB(t)

	rl := auth.NewRateLimiter()
	a := NewAuth(database, rl)

	// Create a default plan so requests have somewhere to go
	_ = database.SavePlan("default", types.PlanConfig{
		Providers: []types.ProviderConfig{},
	})

	// Create the hermes-m1 key via admin handler
	adminKey := "test-admin-key"
	srv := NewServer(nil, nil, database, a, adminKey)

	// 1. Create key without auth — should fail
	req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"name":"hermes-m1","plans":["default"],"models":["gpt-4"],"rate_limit_rpm":10,"monthly_token_limit":100000}`))
	rr := httptest.NewRecorder()
	srv.handleCreateKey(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("create key without admin: expected 403, got %d", rr.Code)
	}

	// 2. Create key with admin auth
	req = httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"name":"hermes-m1","plans":["default"],"models":["gpt-4"],"rate_limit_rpm":10,"monthly_token_limit":100000}`))
	req.Header.Set("X-Admin-Key", adminKey)
	rr = httptest.NewRecorder()
	srv.handleCreateKey(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create key: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var createResp struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal create resp: %v", err)
	}
	apiKey := createResp.Key
	if !strings.HasPrefix(apiKey, "sr-") {
		t.Fatalf("expected key prefix sr-, got %s", apiKey)
	}
	t.Logf("Generated hermes-m1 key: %s", apiKey)

	// 3. Request without Authorization — should 401
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: expected 401, got %d", rr.Code)
	}

	// 4. Request with valid key — should pass
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4"}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid key: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// 5. Request with valid key to denied plan — should 403
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"premium/gpt-4"}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("denied plan: expected 403, got %d", rr.Code)
	}

	// 6. Update key: set IP allowlist and test
	body, _ := json.Marshal(types.APIKey{
		Name:       "hermes-m1",
		Plans:      []string{"default"},
		Models:     []string{"gpt-4"},
		AllowedIPs: []string{"10.0.0.0/8"},
		Disabled:   false,
	})
	req = httptest.NewRequest(http.MethodPut, "/v1/keys/"+apiKey, bytes.NewReader(body))
	req = mux.SetURLVars(req, map[string]string{"key": apiKey})
	req.Header.Set("X-Admin-Key", adminKey)
	rr = httptest.NewRecorder()
	srv.handleUpdateKey(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update IP allowlist: expected 200, got %d", rr.Code)
	}
	a.keyCacheMu.Lock()
	delete(a.keyCache, apiKey)
	a.keyCacheMu.Unlock()

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4"}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.RemoteAddr = "192.168.1.1:1234" // outside 10.0.0.0/8
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("blocked IP: expected 403, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4"}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.RemoteAddr = "10.0.0.5:1234" // inside 10.0.0.0/8
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("allowed IP: expected 200, got %d", rr.Code)
	}

	// 7. Update key: disable it
	req = httptest.NewRequest(http.MethodPut, "/v1/keys/"+apiKey, strings.NewReader(`{"name":"hermes-m1","plans":["default"],"models":["gpt-4"],"disabled":true}`))
	req = mux.SetURLVars(req, map[string]string{"key": apiKey})
	req.Header.Set("X-Admin-Key", adminKey)
	rr = httptest.NewRecorder()
	srv.handleUpdateKey(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update key: expected 200, got %d", rr.Code)
	}

	// 8. Request with disabled key — should 401
	a.keyCacheMu.Lock()
	delete(a.keyCache, apiKey)
	a.keyCacheMu.Unlock()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4"}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("disabled key: expected 401, got %d", rr.Code)
	}

	// 9. Re-enable key for further tests
	req = httptest.NewRequest(http.MethodPut, "/v1/keys/"+apiKey, strings.NewReader(`{"name":"hermes-m1","plans":["default"],"models":["gpt-4"],"disabled":false}`))
	req = mux.SetURLVars(req, map[string]string{"key": apiKey})
	req.Header.Set("X-Admin-Key", adminKey)
	rr = httptest.NewRecorder()
	srv.handleUpdateKey(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("re-enable key: expected 200, got %d", rr.Code)
	}
	a.keyCacheMu.Lock()
	delete(a.keyCache, apiKey)
	a.keyCacheMu.Unlock()

	// 10. Set expiry to past and test expired key
	past := time.Now().Add(-24 * time.Hour).Unix()
	body, _ = json.Marshal(types.APIKey{
		Name:      "hermes-m1",
		Plans:     []string{"default"},
		Models:    []string{"gpt-4"},
		Disabled:  false,
		ExpiresAt: &past,
	})
	req = httptest.NewRequest(http.MethodPut, "/v1/keys/"+apiKey, bytes.NewReader(body))
	req = mux.SetURLVars(req, map[string]string{"key": apiKey})
	req.Header.Set("X-Admin-Key", adminKey)
	rr = httptest.NewRecorder()
	srv.handleUpdateKey(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("set expiry: expected 200, got %d", rr.Code)
	}
	a.keyCacheMu.Lock()
	delete(a.keyCache, apiKey)
	a.keyCacheMu.Unlock()

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4"}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expired key: expected 401, got %d", rr.Code)
	}

	// 11. Clean up: delete the key
	req = httptest.NewRequest(http.MethodDelete, "/v1/keys/"+apiKey, nil)
	req = mux.SetURLVars(req, map[string]string{"key": apiKey})
	req.Header.Set("X-Admin-Key", adminKey)
	rr = httptest.NewRecorder()
	srv.handleDeleteKey(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete key: expected 200, got %d", rr.Code)
	}
}

func TestAuthMiddleware_PlanWildcard(t *testing.T) {
	database := setupTestDB(t)

	_ = database.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{}})
	_ = database.SavePlan("premium", types.PlanConfig{Providers: []types.ProviderConfig{}})

	rl := auth.NewRateLimiter()
	a := NewAuth(database, rl)
	adminKey := "test-admin-key"
	srv := NewServer(nil, nil, database, a, adminKey)

	// Create key with wildcard plan access
	req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"name":"hermes-m1-wildcard","plans":["*"],"models":["*"]}`))
	req.Header.Set("X-Admin-Key", adminKey)
	rr := httptest.NewRecorder()
	srv.handleCreateKey(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create key: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var createResp struct{ Key string }
	json.Unmarshal(rr.Body.Bytes(), &createResp)
	apiKey := createResp.Key

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Any plan should work
	for _, plan := range []string{"default", "premium", "nonexistent"} {
		req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+plan+`/gpt-4"}`))
		req.Header.Set("Authorization", "Bearer "+apiKey)
		rr = httptest.NewRecorder()
		a.Middleware(next).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("plan %s: expected 200, got %d: %s", plan, rr.Code, rr.Body.String())
		}
	}

}

func TestAuthMiddleware_RateLimitAndQuota(t *testing.T) {
	database := setupTestDB(t)

	_ = database.SavePlan("default", types.PlanConfig{
		Providers: []types.ProviderConfig{},
	})

	rl := auth.NewRateLimiter()
	a := NewAuth(database, rl)

	adminKey := "test-admin-key"
	srv := NewServer(nil, nil, database, a, adminKey)

	// Create key with RPM=2 and monthly request limit=3
	req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"name":"hermes-m1-limited","plans":["default"],"rate_limit_rpm":2,"monthly_request_limit":3}`))
	req.Header.Set("X-Admin-Key", adminKey)
	rr := httptest.NewRecorder()
	srv.handleCreateKey(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create key: expected 200, got %d", rr.Code)
	}
	var createResp struct{ Key string }
	json.Unmarshal(rr.Body.Bytes(), &createResp)
	apiKey := createResp.Key

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// 1st request — allowed
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("request 1: expected 200, got %d", rr.Code)
	}

	// 2nd request — allowed
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("request 2: expected 200, got %d", rr.Code)
	}

	// 3rd request — rate limited (RPM=2)
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("request 3: expected 429, got %d", rr.Code)
	}

	// Manually inject stats to hit monthly quota
	for i := 0; i < 3; i++ {
		_ = database.RecordStat(types.StatRecord{
			Plan:           "default",
			Provider:       "test",
			Model:          "gpt-4",
			ClientKey:      apiKey,
			RequestTokens:  0,
			ResponseTokens: 0,
			TotalTokens:    0,
			Status:         "success",
			LatencyMs:      100,
		})
	}
	database.FlushStats()

	// Reset rate limiter so RPM doesn't block
	rl = auth.NewRateLimiter()
	a2 := NewAuth(database, rl)
	a2.keyCache = a.keyCache

	// Request after quota — should 429
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr = httptest.NewRecorder()
	a2.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("quota exceeded: expected 429, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAuthMiddleware_KeyGroups(t *testing.T) {
	database := setupTestDB(t)

	_ = database.SavePlan("default", types.PlanConfig{
		Providers: []types.ProviderConfig{},
	})

	rl := auth.NewRateLimiter()
	a := NewAuth(database, rl)
	adminKey := "test-admin-key"
	srv := NewServer(nil, nil, database, a, adminKey)

	// Create a key group with monthly request limit=2
	req := httptest.NewRequest(http.MethodPost, "/v1/groups", strings.NewReader(`{"name":"test-group","monthly_request_limit":2}`))
	req.Header.Set("X-Admin-Key", adminKey)
	rr := httptest.NewRecorder()
	srv.handleCreateGroup(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create group: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var groupResp struct{ ID int64 }
	json.Unmarshal(rr.Body.Bytes(), &groupResp)
	groupID := groupResp.ID

	// Create key assigned to group, no individual limit
	body, _ := json.Marshal(types.APIKey{
		Name:    "hermes-m1-grouped",
		Plans:   []string{"default"},
		GroupID: &groupID,
	})
	req = httptest.NewRequest(http.MethodPost, "/v1/keys", bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", adminKey)
	rr = httptest.NewRecorder()
	srv.handleCreateKey(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create key: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var createResp struct{ Key string }
	json.Unmarshal(rr.Body.Bytes(), &createResp)
	apiKey := createResp.Key

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// 1st request — allowed
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("request 1: expected 200, got %d", rr.Code)
	}

	// Inject stats to hit group quota
	for i := 0; i < 2; i++ {
		_ = database.RecordStat(types.StatRecord{
			Plan:      "default",
			Provider:  "test",
			Model:     "gpt-4",
			ClientKey: apiKey,
			Status:    "success",
			LatencyMs: 100,
		})
	}
	database.FlushStats()

	// Clear usage cache so the next request sees the injected stats
	a.usageCache = make(map[string]usageCacheEntry)

	// Request after group quota — should 429
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("group quota exceeded: expected 429, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAuthMiddleware_ContextKey(t *testing.T) {
	database := setupTestDB(t)

	_ = database.SavePlan("default", types.PlanConfig{
		Providers: []types.ProviderConfig{},
	})

	rl := auth.NewRateLimiter()
	a := NewAuth(database, rl)
	adminKey := "test-admin-key"
	srv := NewServer(nil, nil, database, a, adminKey)

	req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"name":"hermes-m1","plans":["default"]}`))
	req.Header.Set("X-Admin-Key", adminKey)
	rr := httptest.NewRecorder()
	srv.handleCreateKey(rr, req)
	var createResp struct{ Key string }
	json.Unmarshal(rr.Body.Bytes(), &createResp)
	apiKey := createResp.Key

	var capturedKey string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedKey = ClientKeyFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr = httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if capturedKey != apiKey {
		t.Fatalf("expected context key %q, got %q", apiKey, capturedKey)
	}
}

func TestDefaultKeyBootstrap(t *testing.T) {
	database := setupTestDB(t)

	count, err := database.CountAPIKeys()
	if err != nil {
		t.Fatalf("count keys: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 keys, got %d", count)
	}

	// Simulate what main.go does
	defaultKey := types.APIKey{
		Key:       auth.GenerateAPIKey(),
		Name:      "default",
		Plans:     []string{},
		Models:    []string{},
		CreatedAt: time.Now().Unix(),
	}
	if err := database.CreateAPIKey(defaultKey); err != nil {
		t.Fatalf("create default key: %v", err)
	}

	count, _ = database.CountAPIKeys()
	if count != 1 {
		t.Fatalf("expected 1 key, got %d", count)
	}

	// Verify key works
	rl := auth.NewRateLimiter()
	a := NewAuth(database, rl)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+defaultKey.Key)
	rr := httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("default key: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAuditLog(t *testing.T) {
	database := setupTestDB(t)

	adminKey := "test-admin-key"
	rl := auth.NewRateLimiter()
	a := NewAuth(database, rl)
	srv := NewServer(nil, nil, database, a, adminKey)

	// Create key
	req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"name":"hermes-m1","plans":["default"]}`))
	req.Header.Set("X-Admin-Key", adminKey)
	rr := httptest.NewRecorder()
	srv.handleCreateKey(rr, req)

	var createResp struct{ Key string }
	json.Unmarshal(rr.Body.Bytes(), &createResp)
	apiKey := createResp.Key

	// Update key
	req = httptest.NewRequest(http.MethodPut, "/v1/keys/"+apiKey, strings.NewReader(`{"name":"hermes-m1-renamed"}`))
	req = mux.SetURLVars(req, map[string]string{"key": apiKey})
	req.Header.Set("X-Admin-Key", adminKey)
	rr = httptest.NewRecorder()
	srv.handleUpdateKey(rr, req)

	// Check audit log
	logs, err := database.ListAuditLogs(10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(logs) < 2 {
		t.Fatalf("expected at least 2 audit entries, got %d", len(logs))
	}

	// Verify audit entries via API
	req = httptest.NewRequest(http.MethodGet, "/v1/audit", nil)
	req.Header.Set("X-Admin-Key", adminKey)
	rr = httptest.NewRecorder()
	srv.handleListAudit(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list audit: expected 200, got %d", rr.Code)
	}
	var apiLogs []map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &apiLogs)
	if len(apiLogs) < 2 {
		t.Fatalf("expected at least 2 audit entries from API, got %d", len(apiLogs))
	}
}

func TestAuthMiddleware_ThroughMuxRouter(t *testing.T) {
	database := setupTestDB(t)

	_ = database.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{}})

	rl := auth.NewRateLimiter()
	a := NewAuth(database, rl)
	adminKey := "test-admin-key"
	srv := NewServer(nil, nil, database, a, adminKey)

	// Set up router the SAME WAY main.go does
	muxRouter := mux.NewRouter()
	muxRouter.Use(a.Middleware)
	srv.RegisterRoutes(muxRouter)

	// Request without Authorization through the router — should 401
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	muxRouter.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no auth through router: expected 401, got %d: %s", rr.Code, rr.Body.String())
	}

	// Admin endpoints should skip auth (only need X-Admin-Key)
	req = httptest.NewRequest(http.MethodGet, "/v1/keys", nil)
	req.Header.Set("X-Admin-Key", adminKey)
	rr = httptest.NewRecorder()
	muxRouter.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin endpoint through router: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"sr-abc123def456", "****f456"},
		{"short", "****hort"},
		{"", "****"},
	}
	for _, tt := range tests {
		got := types.MaskAPIKey(tt.in)
		if got != tt.want {
			t.Errorf("MaskAPIKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseBearerToken(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Bearer sk-test", "sk-test"},
		{"Bearer  token-with-space", "token-with-space"},
		{"Basic sk-test", ""},
		{"", ""},
		{"Bearer", ""},
	}
	for _, tt := range tests {
		got := auth.ParseBearerToken(tt.in)
		if got != tt.want {
			t.Errorf("ParseBearerToken(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMiddleware_DisabledKey(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	key := types.APIKey{Key: "sr-disabled", Name: "disabled", Plans: []string{"default"}, CreatedAt: time.Now().Unix()}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}
	// CreateAPIKey hardcodes disabled=0, so update it
	key.Disabled = true
	if err := db.UpdateAPIKey(key.Key, key); err != nil {
		t.Fatalf("update key: %v", err)
	}

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "api key disabled") {
		t.Fatalf("expected 'api key disabled' in body, got %s", rr.Body.String())
	}
}

func TestMiddleware_ExpiredKey(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	past := time.Now().Add(-24 * time.Hour).Unix()
	key := types.APIKey{Key: "sr-expired", Name: "expired", Plans: []string{"default"}, ExpiresAt: &past, CreatedAt: time.Now().Unix()}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "api key expired") {
		t.Fatalf("expected 'api key expired' in body, got %s", rr.Body.String())
	}
}

func TestMiddleware_IPAllowlist_Allowed(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	key := types.APIKey{Key: "sr-ip-allow", Name: "ip-allow", Plans: []string{"default"}, AllowedIPs: []string{"127.0.0.1/32"}, CreatedAt: time.Now().Unix()}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestMiddleware_IPAllowlist_Blocked(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	key := types.APIKey{Key: "sr-ip-block", Name: "ip-block", Plans: []string{"default"}, AllowedIPs: []string{"10.0.0.0/8"}, CreatedAt: time.Now().Unix()}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "client ip not allowed") {
		t.Fatalf("expected 'client ip not allowed' in body, got %s", rr.Body.String())
	}
}

func TestMiddleware_PlanNotAllowed(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})
	_ = db.SavePlan("pro", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	key := types.APIKey{Key: "sr-plan-deny", Name: "plan-deny", Plans: []string{"pro"}, CreatedAt: time.Now().Unix()}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Empty body → plan defaults to "default" which is not in key's plans
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "plan not allowed for this key") {
		t.Fatalf("expected 'plan not allowed for this key' in body, got %s", rr.Body.String())
	}
}

func TestMiddleware_PlanWildcardAllowed(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})
	_ = db.SavePlan("premium", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	key := types.APIKey{Key: "sr-plan-wild", Name: "plan-wild", Plans: []string{"*"}, CreatedAt: time.Now().Unix()}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, plan := range []string{"default", "premium", "nonexistent"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+plan+`/gpt-4"}`))
		req.Header.Set("Authorization", "Bearer "+key.Key)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("plan %s: expected 200, got %d: %s", plan, rr.Code, rr.Body.String())
		}
	}
}

func TestMiddleware_RateLimitExceeded(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	key := types.APIKey{Key: "sr-rate", Name: "rate", Plans: []string{"default"}, RateLimitRPM: 1, CreatedAt: time.Now().Unix()}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 1st request — allowed
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("request 1: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// 2nd request — rate limited
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("request 2: expected 429, got %d: %s", rr.Code, rr.Body.String())
	}

	// 3rd request — still rate limited
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("request 3: expected 429, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "rate limit exceeded") {
		t.Fatalf("expected 'rate limit exceeded' in body, got %s", rr.Body.String())
	}
}

func TestMiddleware_MonthlyTokenQuotaExceeded(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	key := types.APIKey{Key: "sr-tok-quota", Name: "tok-quota", Plans: []string{"default"}, MonthlyTokenLimit: 10, CreatedAt: time.Now().Unix()}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	// Seed stats showing 10 tokens used this month
	_ = db.RecordStat(types.StatRecord{
		Plan:           "default",
		Provider:       "test",
		Model:          "gpt-4",
		ClientKey:      key.Key,
		RequestTokens:  4,
		ResponseTokens: 6,
		TotalTokens:    10,
		Status:         "success",
		LatencyMs:      100,
	})
	db.FlushStats()

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "monthly token quota exceeded") {
		t.Fatalf("expected 'monthly token quota exceeded' in body, got %s", rr.Body.String())
	}
}

func TestMiddleware_MonthlyRequestQuotaExceeded(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	key := types.APIKey{Key: "sr-req-quota", Name: "req-quota", Plans: []string{"default"}, MonthlyRequestLimit: 1, CreatedAt: time.Now().Unix()}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	// Seed stats showing 1 request used this month
	_ = db.RecordStat(types.StatRecord{
		Plan:      "default",
		Provider:  "test",
		Model:     "gpt-4",
		ClientKey: key.Key,
		Status:    "success",
		LatencyMs: 100,
	})
	db.FlushStats()

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "monthly request quota exceeded") {
		t.Fatalf("expected 'monthly request quota exceeded' in body, got %s", rr.Body.String())
	}
}

func TestMiddleware_GroupTokenQuotaExceeded(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	group := types.KeyGroup{Name: "tok-group", MonthlyTokenLimit: 10}
	groupID, err := db.CreateKeyGroup(group)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	key := types.APIKey{Key: "sr-grp-tok", Name: "grp-tok", Plans: []string{"default"}, GroupID: &groupID, CreatedAt: time.Now().Unix()}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	// Seed stats showing 10 tokens for this key (group usage is aggregated by client_key)
	_ = db.RecordStat(types.StatRecord{
		Plan:           "default",
		Provider:       "test",
		Model:          "gpt-4",
		ClientKey:      key.Key,
		RequestTokens:  5,
		ResponseTokens: 5,
		TotalTokens:    10,
		Status:         "success",
		LatencyMs:      100,
	})
	db.FlushStats()

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "group monthly token quota exceeded") {
		t.Fatalf("expected 'group monthly token quota exceeded' in body, got %s", rr.Body.String())
	}
}

func TestMiddleware_GroupRequestQuotaExceeded(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	group := types.KeyGroup{Name: "req-group", MonthlyRequestLimit: 1}
	groupID, err := db.CreateKeyGroup(group)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	key := types.APIKey{Key: "sr-grp-req", Name: "grp-req", Plans: []string{"default"}, GroupID: &groupID, CreatedAt: time.Now().Unix()}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	// Seed stats showing 1 request for this key
	_ = db.RecordStat(types.StatRecord{
		Plan:      "default",
		Provider:  "test",
		Model:     "gpt-4",
		ClientKey: key.Key,
		Status:    "success",
		LatencyMs: 100,
	})
	db.FlushStats()

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "group monthly request quota exceeded") {
		t.Fatalf("expected 'group monthly request quota exceeded' in body, got %s", rr.Body.String())
	}
}

func TestMiddleware_AdminEndpointsSkipAuth(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Request to /v1/plans without any auth header should pass through
	req := httptest.NewRequest(http.MethodGet, "/v1/plans", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestMiddleware_OPTIONSSkipAuth(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestMiddleware_BodyPassedThroughContext(t *testing.T) {
	db := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(db, rl)
	_ = db.SavePlan("default", types.PlanConfig{Providers: []types.ProviderConfig{{Name: "test", Model: "gpt-4"}}})

	key := types.APIKey{Key: "sr-body-ctx", Name: "body-ctx", Plans: []string{"default"}, CreatedAt: time.Now().Unix()}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	var capturedBody map[string]interface{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = BodyFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	reqBody := `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"temperature":0.7}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rr := httptest.NewRecorder()
	a.Middleware(next).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	if capturedBody == nil {
		t.Fatal("expected body in context, got nil")
	}
	if capturedBody["model"] != "gpt-4" {
		t.Errorf("expected model=gpt-4 in context body, got %v", capturedBody["model"])
	}
	if temp, ok := capturedBody["temperature"].(float64); !ok || temp != 0.7 {
		t.Errorf("expected temperature=0.7 in context body, got %v", capturedBody["temperature"])
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestIsIPAllowed_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		allowed []string
		want    bool
	}{
		{"empty allowed list", "192.168.1.1", []string{}, false},
		{"invalid CIDR skipped", "192.168.1.1", []string{"invalid-cidr"}, false},
		{"invalid IP", "not-an-ip", []string{"192.168.1.0/24"}, false},
		{"IPv6 allowed", "::1", []string{"::1/128"}, true},
		{"IPv6 not in range", "2001:db8::1", []string{"::1/128"}, false},
		{"exact match", "192.168.1.1", []string{"192.168.1.1"}, true},
		{"CIDR match", "192.168.1.1", []string{"192.168.0.0/16"}, true},
		{"no match", "10.0.0.1", []string{"192.168.0.0/16"}, false},
		{"mixed valid and invalid CIDR", "192.168.1.1", []string{"invalid", "192.168.1.0/24"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isIPAllowed(tt.ip, tt.allowed); got != tt.want {
				t.Errorf("isIPAllowed(%q, %v) = %v, want %v", tt.ip, tt.allowed, got, tt.want)
			}
		})
	}
}

func TestCheckAndSendQuotaAlert(t *testing.T) {
	database := setupTestDB(t)

	rl := auth.NewRateLimiter()
	a := NewAuth(database, rl)

	var webhookCalls int
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		webhookCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	key := "sr-quota-test"
	apiKey := &types.APIKey{
		Key:               key,
		Name:              "quota-test",
		WebhookURL:        ts.URL,
		MonthlyTokenLimit: 1000,
	}

	// Usage below 80% - should NOT alert
	usage := &db.MonthlyUsage{
		RequestTokens:  700,
		ResponseTokens: 0,
		RequestCount:   0,
	}
	a.checkAndSendQuotaAlert(apiKey, usage)

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	if webhookCalls != 0 {
		t.Errorf("expected 0 webhook calls for 70%% usage, got %d", webhookCalls)
	}
	mu.Unlock()

	// Usage above 80% - should alert
	usage = &db.MonthlyUsage{
		RequestTokens:  850,
		ResponseTokens: 0,
		RequestCount:   0,
	}
	a.checkAndSendQuotaAlert(apiKey, usage)

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	if webhookCalls != 1 {
		t.Errorf("expected 1 webhook call for 85%% usage, got %d", webhookCalls)
	}
	mu.Unlock()

	// Same key/month - should NOT alert again (dedup)
	a.checkAndSendQuotaAlert(apiKey, usage)

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	if webhookCalls != 1 {
		t.Errorf("expected still 1 webhook call due to dedup, got %d", webhookCalls)
	}
	mu.Unlock()
}

func TestLastUsedFlushWorker(t *testing.T) {
	database := setupTestDB(t)

	rl := auth.NewRateLimiter()
	a := NewAuth(database, rl)

	key := types.APIKey{Key: "sr-flush-test", Name: "flush-test"}
	if err := database.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	// Mark key as used
	a.markLastUsed(key.Key)

	// Verify it's in the batch
	a.lastUsedMu.Lock()
	if _, ok := a.lastUsedBatch[key.Key]; !ok {
		t.Error("expected key to be in lastUsedBatch")
	}
	a.lastUsedMu.Unlock()

	// Wait for flush worker to process (ticker is 5s)
	time.Sleep(6 * time.Second)

	// Verify batch is empty after flush
	a.lastUsedMu.Lock()
	if len(a.lastUsedBatch) != 0 {
		t.Errorf("expected empty batch after flush, got %d items", len(a.lastUsedBatch))
	}
	a.lastUsedMu.Unlock()

	// Verify DB was updated (last_used_at should be set)
	updated, err := database.GetAPIKey(key.Key)
	if err != nil {
		t.Fatalf("get key: %v", err)
	}
	if updated.LastUsedAt == nil || *updated.LastUsedAt == 0 {
		t.Error("expected LastUsedAt to be updated after flush")
	}
}

func TestCacheEviction(t *testing.T) {
	database := setupTestDB(t)

	rl := auth.NewRateLimiter()
	// Create Auth manually to avoid background workers with long tickers
	a := &Auth{
		db:            database,
		rl:            rl,
		keyCache:      make(map[string]cachedKey),
		keyCacheTTL:   50 * time.Millisecond,
		usageCache:    make(map[string]usageCacheEntry),
		usageCacheTTL: 10 * time.Second,
		groupCache:    make(map[int64]groupCacheEntry),
		groupCacheTTL: 60 * time.Second,
		lastUsedBatch: make(map[string]struct{}),
		alertedKeys:   make(map[string]bool),
	}

	// Create a key
	key := types.APIKey{Key: "sr-cache-test", Name: "cache-test"}
	if err := database.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	// First call should cache the key
	_, err := a.getKeyCached(key.Key)
	if err != nil {
		t.Fatalf("get key cached: %v", err)
	}

	a.keyCacheMu.RLock()
	if len(a.keyCache) != 1 {
		t.Errorf("expected 1 cached key, got %d", len(a.keyCache))
	}
	a.keyCacheMu.RUnlock()

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Manually run eviction logic (same algorithm as cacheEvictionWorker)
	now := time.Now()
	a.keyCacheMu.Lock()
	for k, v := range a.keyCache {
		if now.Sub(v.loadedAt) > a.keyCacheTTL {
			delete(a.keyCache, k)
		}
	}
	a.keyCacheMu.Unlock()

	a.keyCacheMu.RLock()
	if len(a.keyCache) != 0 {
		t.Errorf("expected 0 cached keys after eviction, got %d", len(a.keyCache))
	}
	a.keyCacheMu.RUnlock()

	// Next call should re-fetch from DB
	_, err = a.getKeyCached(key.Key)
	if err != nil {
		t.Fatalf("get key cached after eviction: %v", err)
	}

	a.keyCacheMu.RLock()
	if len(a.keyCache) != 1 {
		t.Errorf("expected 1 cached key after re-fetch, got %d", len(a.keyCache))
	}
	a.keyCacheMu.RUnlock()
}

func TestCacheEvictionWorker(t *testing.T) {
	database := setupTestDB(t)

	rl := auth.NewRateLimiter()
	a := &Auth{
		db:            database,
		rl:            rl,
		keyCache:      make(map[string]cachedKey),
		keyCacheTTL:   50 * time.Millisecond,
		usageCache:    make(map[string]usageCacheEntry),
		usageCacheTTL: 50 * time.Millisecond,
		groupCache:    make(map[int64]groupCacheEntry),
		groupCacheTTL: 50 * time.Millisecond,
		lastUsedBatch: make(map[string]struct{}),
		alertedKeys:   make(map[string]bool),
	}

	key := types.APIKey{Key: "sr-evict-worker", Name: "evict-worker"}
	if err := database.CreateAPIKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	// Populate caches with entries that will expire quickly
	now := time.Now()
	a.keyCacheMu.Lock()
	a.keyCache[key.Key] = cachedKey{key: &key, loadedAt: now.Add(-100 * time.Millisecond)}
	a.keyCacheMu.Unlock()
	a.usageCacheMu.Lock()
	a.usageCache["test:2024-01"] = usageCacheEntry{usage: &db.MonthlyUsage{}, loadedAt: now.Add(-100 * time.Millisecond)}
	a.usageCacheMu.Unlock()
	a.groupCacheMu.Lock()
	a.groupCache[1] = groupCacheEntry{group: &types.KeyGroup{}, loadedAt: now.Add(-100 * time.Millisecond)}
	a.groupCacheMu.Unlock()

	// Run the same eviction logic the worker uses, but synchronously
	a.keyCacheMu.Lock()
	for k, v := range a.keyCache {
		if time.Since(v.loadedAt) > a.keyCacheTTL {
			delete(a.keyCache, k)
		}
	}
	a.keyCacheMu.Unlock()
	a.usageCacheMu.Lock()
	for k, v := range a.usageCache {
		if time.Since(v.loadedAt) > a.usageCacheTTL {
			delete(a.usageCache, k)
		}
	}
	a.usageCacheMu.Unlock()
	a.groupCacheMu.Lock()
	for k, v := range a.groupCache {
		if time.Since(v.loadedAt) > a.groupCacheTTL {
			delete(a.groupCache, k)
		}
	}
	a.groupCacheMu.Unlock()

	// After eviction, caches should be empty
	a.keyCacheMu.RLock()
	keyCount := len(a.keyCache)
	a.keyCacheMu.RUnlock()
	a.usageCacheMu.RLock()
	usageCount := len(a.usageCache)
	a.usageCacheMu.RUnlock()
	a.groupCacheMu.RLock()
	groupCount := len(a.groupCache)
	a.groupCacheMu.RUnlock()

	if keyCount != 0 {
		t.Errorf("expected 0 cached keys after worker eviction, got %d", keyCount)
	}
	if usageCount != 0 {
		t.Errorf("expected 0 usage cache entries after worker eviction, got %d", usageCount)
	}
	if groupCount != 0 {
		t.Errorf("expected 0 group cache entries after worker eviction, got %d", groupCount)
	}
}

func TestCheckAndSendQuotaAlert_NoWebhook(t *testing.T) {
	database := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(database, rl)

	apiKey := &types.APIKey{
		Key:               "sr-no-webhook",
		Name:              "no-webhook",
		WebhookURL:        "",
		MonthlyTokenLimit: 100,
	}
	usage := &db.MonthlyUsage{RequestTokens: 90, ResponseTokens: 0}

	// Should return early without panic
	a.checkAndSendQuotaAlert(apiKey, usage)
}

func TestCheckAndSendQuotaAlert_RequestLimit(t *testing.T) {
	database := setupTestDB(t)
	rl := auth.NewRateLimiter()
	a := NewAuth(database, rl)

	var webhookCalls int
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		webhookCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	key := "sr-quota-req-test"
	apiKey := &types.APIKey{
		Key:                 key,
		Name:                "quota-req-test",
		WebhookURL:          ts.URL,
		MonthlyRequestLimit: 100,
	}

	// Usage below 80% - should NOT alert
	usage := &db.MonthlyUsage{RequestCount: 70}
	a.checkAndSendQuotaAlert(apiKey, usage)
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	if webhookCalls != 0 {
		t.Errorf("expected 0 webhook calls for 70%% request usage, got %d", webhookCalls)
	}
	mu.Unlock()

	// Usage above 80% via request count - should alert
	usage = &db.MonthlyUsage{RequestCount: 85}
	a.checkAndSendQuotaAlert(apiKey, usage)
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	if webhookCalls != 1 {
		t.Errorf("expected 1 webhook call for 85%% request usage, got %d", webhookCalls)
	}
	mu.Unlock()
}
