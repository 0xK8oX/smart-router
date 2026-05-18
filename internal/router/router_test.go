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

	resp, provider, err := router.Route("pro", body, false, "openai", nil)
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

	// Success stats are now recorded by the HTTP handler, not the router.
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

	resp, provider, err := router.Route("pro", body, false, "openai", nil)
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

	// Verify failure stat recorded for the first (bad) provider.
	// Success stats are recorded by the HTTP handler, not the router.
	database.FlushStats()
	stats, err := database.GetStats("pro", "", 10)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 failure stat, got %d", len(stats))
	}
	if stats[0].Status != "failure" {
		t.Errorf("expected status failure, got %s", stats[0].Status)
	}
	if stats[0].Provider != "openai-bad" {
		t.Errorf("expected provider openai-bad, got %s", stats[0].Provider)
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

	resp, _, err := router.Route("pro", body, false, "openai", nil)
	if err == nil {
		t.Fatal("expected error when all providers fail, got nil")
	}
	if resp != nil {
		resp.Body.Close()
		t.Fatal("expected nil response when all providers fail")
	}

	// Verify failure stat recorded
	database.FlushStats()
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

	resp, provider, err := router.Route("pro", body, false, "openai", nil)
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

	// Success stats are recorded by the HTTP handler, not the router.
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

	resp, _, err := router.Route("pro", body, false, "openai", nil)
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

	resp, _, err := router.Route("pro", body, true, "openai", nil)
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

	// Streaming flag verified by handler stat recording, not router.
}

func TestRouteVirtualProvider(t *testing.T) {
	// Target plan server returns 200
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"virtual-test","object":"chat.completion","choices":[]}`))
	}))
	defer targetServer.Close()

	sqlitePath := "/tmp/test_router_virtual.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-virtual-*")
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

	// Save target plan with a real provider
	targetPlan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{
				Name:    "real-provider",
				BaseURL: targetServer.URL,
				Model:   "gpt-4",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-test",
			},
		},
	}
	if err := database.SavePlan("target", targetPlan); err != nil {
		t.Fatalf("save target plan: %v", err)
	}

	// Save outer plan with virtual provider
	outerPlan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{
				Name:    "auto-chat2api",
				BaseURL: "smart://target",
				Model:   "auto",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "",
			},
		},
	}
	if err := database.SavePlan("outer", outerPlan); err != nil {
		t.Fatalf("save outer plan: %v", err)
	}

	router := New(ht, database)

	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, provider, err := router.Route("outer", body, false, "openai", nil)
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
	if provider.Name != "real-provider" {
		t.Errorf("expected provider real-provider from target plan, got %s", provider.Name)
	}
}

func TestRouteVirtualProviderMaxDepth(t *testing.T) {
	sqlitePath := "/tmp/test_router_virtual_depth.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-virtual-depth-*")
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

	// Plan A → smart://B
	// Plan B → smart://C
	// Plan C → smart://A (loop)
	for _, pair := range []struct{ name, target string }{
		{"plan-a", "smart://plan-b"},
		{"plan-b", "smart://plan-c"},
		{"plan-c", "smart://plan-a"},
	} {
		plan := types.PlanConfig{
			Providers: []types.ProviderConfig{
				{
					Name:    "virtual",
					BaseURL: pair.target,
					Model:   "auto",
					Format:  "openai",
					Timeout: 5,
				},
			},
		}
		if err := database.SavePlan(pair.name, plan); err != nil {
			t.Fatalf("save plan %s: %v", pair.name, err)
		}
	}

	router := New(ht, database)

	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, _, err := router.Route("plan-a", body, false, "openai", nil)
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("expected error for max depth exceeded, got nil")
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestRouteVirtualProviderFailover(t *testing.T) {
	// Target plan server returns 200
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"virtual-failover","object":"chat.completion","choices":[]}`))
	}))
	defer targetServer.Close()

	sqlitePath := "/tmp/test_router_virtual_failover.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-virtual-failover-*")
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

	// Save target plan with a real provider
	targetPlan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{
				Name:    "real-provider",
				BaseURL: targetServer.URL,
				Model:   "gpt-4",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-test",
			},
		},
	}
	if err := database.SavePlan("target", targetPlan); err != nil {
		t.Fatalf("save target plan: %v", err)
	}

	// Save outer plan: virtual first, then real fallback
	outerPlan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{
				Name:    "auto-chat2api",
				BaseURL: "smart://target",
				Model:   "auto",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "",
			},
			{
				Name:    "fallback",
				BaseURL: targetServer.URL,
				Model:   "gpt-3.5",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-test",
			},
		},
	}
	if err := database.SavePlan("outer", outerPlan); err != nil {
		t.Fatalf("save outer plan: %v", err)
	}

	router := New(ht, database)

	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	resp, provider, err := router.Route("outer", body, false, "openai", nil)
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
	if provider.Name != "real-provider" {
		t.Errorf("expected provider real-provider from virtual redirect, got %s", provider.Name)
	}
}

func TestRouteRoundRobin(t *testing.T) {
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"rr1"}`))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"rr2"}`))
	}))
	defer server2.Close()

	sqlitePath := "/tmp/test_router_rr.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-rr-*")
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
		Strategy: "round_robin",
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
	if err := database.SavePlan("rr", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	router := New(ht, database)

	body := map[string]interface{}{
		"model":    "other",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	// First request should hit provider-a
	resp1, provider1, err := router.Route("rr", body, false, "openai", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp1.Body.Close()

	// Second request should hit provider-b
	resp2, provider2, err := router.Route("rr", body, false, "openai", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp2.Body.Close()

	// Third request should hit provider-a again
	resp3, provider3, err := router.Route("rr", body, false, "openai", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp3.Body.Close()

	if provider1.Name != "provider-a" {
		t.Errorf("expected provider-a first, got %s", provider1.Name)
	}
	if provider2.Name != "provider-b" {
		t.Errorf("expected provider-b second, got %s", provider2.Name)
	}
	if provider3.Name != "provider-a" {
		t.Errorf("expected provider-a third, got %s", provider3.Name)
	}
}

func TestRouteWeightedRoundRobin(t *testing.T) {
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"wrr1"}`))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"wrr2"}`))
	}))
	defer server2.Close()

	sqlitePath := "/tmp/test_router_wrr.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-wrr-*")
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
		Strategy: "weighted_round_robin",
		Providers: []types.ProviderConfig{
			{
				Name:    "provider-a",
				BaseURL: server1.URL,
				Model:   "model-a",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-a",
				Weight:  3,
			},
			{
				Name:    "provider-b",
				BaseURL: server2.URL,
				Model:   "model-b",
				Format:  "openai",
				Timeout: 5,
				APIKey:  "sk-b",
				Weight:  1,
			},
		},
	}
	if err := database.SavePlan("wrr", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	router := New(ht, database)

	body := map[string]interface{}{
		"model":    "other",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	// With weights 3:1, first 3 requests should hit provider-a, then provider-b
	names := make([]string, 4)
	for i := 0; i < 4; i++ {
		resp, provider, err := router.Route("wrr", body, false, "openai", nil)
		if err != nil {
			t.Fatalf("unexpected error on req %d: %v", i, err)
		}
		resp.Body.Close()
		names[i] = provider.Name
	}

	expected := []string{"provider-a", "provider-a", "provider-a", "provider-b"}
	for i, exp := range expected {
		if names[i] != exp {
			t.Errorf("req %d: expected %s, got %s", i, exp, names[i])
		}
	}
}

func TestRouteLRU(t *testing.T) {
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"lru1"}`))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"lru2"}`))
	}))
	defer server2.Close()

	sqlitePath := "/tmp/test_router_lru.db"
	_ = os.Remove(sqlitePath)
	badgerDir, _ := os.MkdirTemp("", "router-lru-*")
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
		Strategy: "lru",
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
	if err := database.SavePlan("lru", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	router := New(ht, database)

	body := map[string]interface{}{
		"model":    "other",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}

	// First request: both unused, provider-a first in plan order after LRU sort (equal -> stable-ish)
	resp1, provider1, err := router.Route("lru", body, false, "openai", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp1.Body.Close()

	// Second request: provider-a was just used, provider-b is LRU
	resp2, provider2, err := router.Route("lru", body, false, "openai", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp2.Body.Close()

	// Third request: provider-b was just used, provider-a is LRU
	resp3, provider3, err := router.Route("lru", body, false, "openai", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp3.Body.Close()

	if provider2.Name == provider1.Name {
		t.Errorf("second request should switch provider, got same %s", provider2.Name)
	}
	if provider3.Name != provider1.Name {
		t.Errorf("third request should return to %s, got %s", provider1.Name, provider3.Name)
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

	resp, provider, err := router.Route("pro", body, false, "openai", nil)
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

	// Success stats are recorded by the HTTP handler, not the router.
}
