package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"smart-router/internal/db"
	"smart-router/internal/health"
	"smart-router/internal/types"
)

func TestRouteSuccess(t *testing.T) {
	// Set up mock provider server that returns 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test","object":"chat.completion","choices":[]}`))
	}))
	defer server.Close()

	// Temp directories for DBs
	sqlitePath := "/tmp/test_router_success.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-health-success-*")
	defer os.RemoveAll(badgerDir)
	defer os.Remove(sqlitePath)

	database, err := db.Open(sqlitePath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ht, err := health.New(badgerDir)
	if err != nil {
		t.Fatalf("open health tracker: %v", err)
	}
	defer ht.Close()

	// Seed plan with provider
	plan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{
				Name:    "openai",
				BaseURL: server.URL,
				Model:   "gpt-4",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-test",
			},
		},
	}
	if err := database.SavePlan("pro", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	router := New(ht, database)

	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, provider, err := router.Route("pro", body, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if provider.Name != "openai" {
		t.Errorf("expected provider openai, got %s", provider.Name)
	}

	// Verify stat was recorded
	stats, err := database.GetStats("pro", "openai", 10)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(stats))
	}
	if stats[0].Status != "success" {
		t.Errorf("expected status success, got %s", stats[0].Status)
	}
}

func TestRouteFailover(t *testing.T) {
	// First provider returns 500
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer server1.Close()

	// Second provider returns 200
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test2","object":"chat.completion","choices":[]}`))
	}))
	defer server2.Close()

	sqlitePath := "/tmp/test_router_failover.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-health-failover-*")
	defer os.RemoveAll(badgerDir)
	defer os.Remove(sqlitePath)

	database, err := db.Open(sqlitePath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ht, err := health.New(badgerDir)
	if err != nil {
		t.Fatalf("open health tracker: %v", err)
	}
	defer ht.Close()

	plan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{
				Name:    "openai-bad",
				BaseURL: server1.URL,
				Model:   "gpt-4",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-bad",
			},
			{
				Name:    "openai-good",
				BaseURL: server2.URL,
				Model:   "gpt-4",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-good",
			},
		},
	}
	if err := database.SavePlan("pro", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	router := New(ht, database)

	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, provider, err := router.Route("pro", body, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if provider.Name != "openai-good" {
		t.Errorf("expected provider openai-good, got %s", provider.Name)
	}

	// Verify stats: one failure and one success
	stats, err := database.GetStats("pro", "", 10)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats, got %d", len(stats))
	}

	// Collect statuses and providers (order may vary due to second-level timestamp precision)
	statuses := map[string]bool{}
	providers := map[string]bool{}
	for _, s := range stats {
		statuses[s.Status] = true
		providers[s.Provider] = true
	}
	if !statuses["success"] {
		t.Errorf("expected a success stat")
	}
	if !statuses["failure"] {
		t.Errorf("expected a failure stat")
	}
	if !providers["openai-good"] {
		t.Errorf("expected a stat for openai-good")
	}
	if !providers["openai-bad"] {
		t.Errorf("expected a stat for openai-bad")
	}
}

func TestRouteAllFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"unavailable"}`))
	}))
	defer server.Close()

	sqlitePath := "/tmp/test_router_allfail.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-health-allfail-*")
	defer os.RemoveAll(badgerDir)
	defer os.Remove(sqlitePath)

	database, err := db.Open(sqlitePath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ht, err := health.New(badgerDir)
	if err != nil {
		t.Fatalf("open health tracker: %v", err)
	}
	defer ht.Close()

	plan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{
				Name:    "unavailable",
				BaseURL: server.URL,
				Model:   "gpt-4",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-test",
			},
		},
	}
	if err := database.SavePlan("pro", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	router := New(ht, database)

	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, _, err := router.Route("pro", body, false)
	if err == nil {
		t.Fatal("expected error when all providers fail, got nil")
	}
	if resp != nil {
		resp.Body.Close()
		t.Fatal("expected nil response when all providers fail")
	}

	// Verify failure stat recorded
	stats, err := database.GetStats("pro", "", 10)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(stats))
	}
	if stats[0].Status != "failure" {
		t.Errorf("expected status failure, got %s", stats[0].Status)
	}
}

func TestRouteSkipsUnhealthyProvider(t *testing.T) {
	// First provider is unhealthy
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"bad"}`))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"good"}`))
	}))
	defer server2.Close()

	sqlitePath := "/tmp/test_router_unhealthy.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-health-unhealthy-*")
	defer os.RemoveAll(badgerDir)
	defer os.Remove(sqlitePath)

	database, err := db.Open(sqlitePath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ht, err := health.New(badgerDir)
	if err != nil {
		t.Fatalf("open health tracker: %v", err)
	}
	defer ht.Close()

	// Mark first provider as unhealthy with future cooldown
	// server_error threshold is 2, so need 2 failures
	if err := ht.RecordFailure("unhealthy-provider", 500, "server error"); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	if err := ht.RecordFailure("unhealthy-provider", 500, "server error"); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	plan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{
				Name:    "unhealthy-provider",
				BaseURL: server1.URL,
				Model:   "gpt-4",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-bad",
			},
			{
				Name:    "healthy-provider",
				BaseURL: server2.URL,
				Model:   "gpt-4",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-good",
			},
		},
	}
	if err := database.SavePlan("pro", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	router := New(ht, database)

	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, provider, err := router.Route("pro", body, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	defer resp.Body.Close()

	if provider.Name != "healthy-provider" {
		t.Errorf("expected provider healthy-provider, got %s", provider.Name)
	}

	// Verify only success stat for healthy provider
	stats, err := database.GetStats("pro", "", 10)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(stats))
	}
	if stats[0].Provider != "healthy-provider" {
		t.Errorf("expected provider healthy-provider, got %s", stats[0].Provider)
	}
}

func TestRouteOverridesModelWithProviderConfig(t *testing.T) {
	var receivedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// Parse simple model field from JSON body
		if len(body) > 0 {
			// Quick parse: find "model":"..."
			for i := 0; i < len(body)-8; i++ {
				if string(body[i:i+8]) == `"model":` {
					start := i + 9
					end := start + 1
					for end < len(body) && body[end] != '"' {
						end++
					}
					receivedModel = string(body[start:end])
					break
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test","object":"chat.completion","choices":[]}`))
	}))
	defer server.Close()

	sqlitePath := "/tmp/test_router_model_override.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-model-*")
	defer os.RemoveAll(badgerDir)
	defer os.Remove(sqlitePath)

	database, err := db.Open(sqlitePath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ht, err := health.New(badgerDir)
	if err != nil {
		t.Fatalf("open health tracker: %v", err)
	}
	defer ht.Close()

	// Provider config says model "kimi-k2.6", request says "gpt-4"
	plan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{
				Name:    "volcengine",
				BaseURL: server.URL,
				Model:   "kimi-k2.6",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-test",
			},
		},
	}
	if err := database.SavePlan("pro", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	router := New(ht, database)

	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, _, err := router.Route("pro", body, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}

	if receivedModel != "kimi-k2.6" {
		t.Errorf("expected model to be overridden to 'kimi-k2.6', got '%s'", receivedModel)
	}
}

func TestRouteStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// For streaming, the client sets stream=true in the body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = body
		w.Write([]byte(`{"id":"stream-test"}`))
	}))
	defer server.Close()

	sqlitePath := "/tmp/test_router_streaming.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-health-streaming-*")
	defer os.RemoveAll(badgerDir)
	defer os.Remove(sqlitePath)

	database, err := db.Open(sqlitePath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ht, err := health.New(badgerDir)
	if err != nil {
		t.Fatalf("open health tracker: %v", err)
	}
	defer ht.Close()

	plan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{
				Name:    "openai",
				BaseURL: server.URL,
				Model:   "gpt-4",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-test",
			},
		},
	}
	if err := database.SavePlan("pro", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	router := New(ht, database)

	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, _, err := router.Route("pro", body, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Verify streaming flag in stat
	stats, err := database.GetStats("pro", "", 10)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(stats))
	}
	if !stats[0].IsStreaming {
		t.Errorf("expected is_streaming=true, got false")
	}
}

func TestRouteSelectsMatchingModelProvider(t *testing.T) {
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test1"}`))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test2"}`))
	}))
	defer server2.Close()

	sqlitePath := "/tmp/test_router_model_match.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-model-match-*")
	defer os.RemoveAll(badgerDir)
	defer os.Remove(sqlitePath)

	database, err := db.Open(sqlitePath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ht, err := health.New(badgerDir)
	if err != nil {
		t.Fatalf("open health tracker: %v", err)
	}
	defer ht.Close()

	plan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{
				Name:    "provider-a",
				BaseURL: server1.URL,
				Model:   "model-a",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-a",
			},
			{
				Name:    "provider-b",
				BaseURL: server2.URL,
				Model:   "model-b",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-b",
			},
		},
	}
	if err := database.SavePlan("pro", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	router := New(ht, database)

	// Request model-b should hit provider-b (second in list but matching model)
	body := map[string]interface{}{
		"model":    "model-b",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, provider, err := router.Route("pro", body, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	defer resp.Body.Close()

	if provider.Name != "provider-b" {
		t.Errorf("expected provider-b, got %s", provider.Name)
	}

	// Verify stat recorded for provider-b
	stats, err := database.GetStats("pro", "provider-b", 10)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat for provider-b, got %d", len(stats))
	}
}
