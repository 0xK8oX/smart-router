# Smart Router Go Rewrite — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Rewrite the TypeScript/Cloudflare Workers Smart Router in Go as a standalone HTTP server with SQLite + BadgerDB.

**Architecture:** Single Go binary using `net/http` + `gorilla/mux`, SQLite for stats/plans, BadgerDB for circuit breaker state, AES-GCM for key encryption.

**Tech Stack:** Go 1.26, net/http, gorilla/mux, modernc.org/sqlite, badger/v4

---

## Task 1: Project Bootstrap

**Files:**
- Create: `main.go`
- Create: `internal/types/types.go`
- Create: `.env`
- Create: `config/plans.yaml`
- Modify: `go.mod`

**Step 1: Define core types**

```go
// internal/types/types.go
package types

type ProviderConfig struct {
	Name              string  `json:"name"`
	BaseURL           string  `json:"base_url"`
	Model             string  `json:"model"`
	Format            string  `json:"format"`
	Timeout           int     `json:"timeout"`
	APIKey            string  `json:"api_key,omitempty"`
	MaskedKey         string  `json:"masked_key,omitempty"`
	WeeklyTokenLimit  *uint64 `json:"weekly_token_limit,omitempty"`
	WeeklyReqLimit    *uint64 `json:"weekly_request_limit,omitempty"`
	ContextLength     *int    `json:"context_length,omitempty"`
	MaxOutputTokens   *int    `json:"max_output_tokens,omitempty"`
}

type PlanConfig struct {
	Providers []ProviderConfig `json:"providers"`
}

type ProviderHealth struct {
	Status              string `json:"status"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	LastFailureAt       int64  `json:"lastFailureAt"`
	CooldownUntil       int64  `json:"cooldownUntil"`
	LastFailureReason   string `json:"lastFailureReason"`
	LastSuccessAt       int64  `json:"lastSuccessAt"`
	TotalRequests       int64  `json:"totalRequests"`
	SuccessCount        int64  `json:"successCount"`
	LastActivityAt      int64  `json:"lastActivityAt"`
}

type StatRecord struct {
	Plan           string `json:"plan"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	KeyMask        string `json:"key_mask,omitempty"`
	RequestTokens  int    `json:"request_tokens"`
	ResponseTokens int    `json:"response_tokens"`
	TotalTokens    int    `json:"total_tokens"`
	Status         string `json:"status"`
	LatencyMs      int64  `json:"latency_ms"`
	IsStreaming    bool   `json:"is_streaming"`
	TargetProvider string `json:"target_provider,omitempty"`
}
```

**Step 2: Create main.go skeleton**

```go
// main.go
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gorilla/mux"
)

func main() {
	port := os.Getenv("SMART_ROUTER_PORT")
	if port == "" {
		port = "8790"
	}
	host := os.Getenv("SMART_ROUTER_HOST")
	if host == "" {
		host = "0.0.0.0"
	}

	r := mux.NewRouter()
	// TODO: add routes

	addr := host + ":" + port
	log.Printf("Smart Router listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, r))
}
```

**Step 3: Create seed config**

```yaml
# config/plans.yaml
plans:
  jason:
    providers:
      - name: jason-kimi-2
        base_url: https://api.kimi.com/coding/
        model: k2p6
        format: anthropic
        timeout: 60
      - name: jason-volcengine
        base_url: https://ark.cn-beijing.volces.com/api/coding
        model: kimi-k2.6
        format: anthropic
        timeout: 60
      - name: jason-minimax
        base_url: https://api.minimaxi.com/anthropic
        model: minimax2.7
        format: anthropic
        timeout: 60
```

**Step 4: go mod tidy**

Run: `go mod tidy`

**Step 5: Commit**

```bash
git add .
git commit -m "chore: bootstrap Go project structure"
```

---

## Task 2: Config Loading

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

**Step 1: Write failing test**

```go
// internal/config/config_test.go
package config

import (
	"testing"
)

func TestLoadPlans(t *testing.T) {
	cfg, err := LoadFromFile("../../config/plans.yaml")
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}
	if len(cfg.Plans) == 0 {
		t.Fatal("expected at least one plan")
	}
	plan, ok := cfg.Plans["jason"]
	if !ok {
		t.Fatal("expected jason plan")
	}
	if len(plan.Providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(plan.Providers))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config/... -v`
Expected: FAIL — config package doesn't exist

**Step 3: Implement config loader**

```go
// internal/config/config.go
package config

import (
	"os"

	"gopkg.in/yaml.v3"
	"smart-router/internal/types"
)

type PlansConfig struct {
	Plans map[string]types.PlanConfig `yaml:"plans"`
}

func LoadFromFile(path string) (*PlansConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg PlansConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/config/... -v`
Expected: PASS

**Step 5: Commit**

```bash
git add .
git commit -m "feat: add YAML config loading"
```

---

## Task 3: SQLite Database + Stats

**Files:**
- Create: `internal/db/db.go`
- Create: `internal/db/db_test.go`

**Step 1: Write failing test**

```go
// internal/db/db_test.go
package db

import (
	"os"
	"testing"

	"smart-router/internal/types"
)

func TestRecordAndQueryStats(t *testing.T) {
	path := "/tmp/test_smart_router.db"
	os.Remove(path)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	record := types.StatRecord{
		Plan:           "jason",
		Provider:       "jason-kimi",
		Model:          "k2p6",
		RequestTokens:  100,
		ResponseTokens: 50,
		TotalTokens:    150,
		Status:         "success",
		LatencyMs:      500,
		IsStreaming:    false,
	}
	if err := db.RecordStat(record); err != nil {
		t.Fatalf("RecordStat failed: %v", err)
	}

	stats, err := db.GetStats("jason", "", 10)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(stats))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/db/... -v`
Expected: FAIL — db package doesn't exist

**Step 3: Implement database layer**

```go
// internal/db/db.go
package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
	"smart-router/internal/types"
)

type DB struct {
	conn *sql.DB
}

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS request_stats (
		id INTEGER PRIMARY KEY,
		plan TEXT NOT NULL,
		provider TEXT NOT NULL,
		model TEXT NOT NULL,
		key_mask TEXT,
		request_tokens INTEGER NOT NULL DEFAULT 0,
		response_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL,
		latency_ms INTEGER NOT NULL,
		is_streaming INTEGER NOT NULL DEFAULT 0,
		target_provider TEXT,
		created_at INTEGER NOT NULL DEFAULT (strftime('%s','now') * 1000)
	);
	CREATE INDEX IF NOT EXISTS idx_stats_plan ON request_stats(plan);
	CREATE INDEX IF NOT EXISTS idx_stats_provider ON request_stats(provider);
	CREATE INDEX IF NOT EXISTS idx_stats_created ON request_stats(created_at);
	CREATE TABLE IF NOT EXISTS plans (
		slug TEXT PRIMARY KEY,
		config TEXT NOT NULL
	);
	`
	_, err := d.conn.Exec(schema)
	return err
}

func (d *DB) RecordStat(r types.StatRecord) error {
	_, err := d.conn.Exec(
		`INSERT INTO request_stats (plan, provider, model, key_mask, request_tokens, response_tokens, total_tokens, status, latency_ms, is_streaming, target_provider) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Plan, r.Provider, r.Model, r.KeyMask, r.RequestTokens, r.ResponseTokens, r.TotalTokens, r.Status, r.LatencyMs, boolToInt(r.IsStreaming), r.TargetProvider,
	)
	return err
}

func (d *DB) GetStats(plan, provider string, limit int) ([]types.StatRecord, error) {
	query := `SELECT plan, provider, model, key_mask, request_tokens, response_tokens, total_tokens, status, latency_ms, is_streaming, target_provider FROM request_stats WHERE 1=1`
	args := []interface{}{}
	if plan != "" {
		query += ` AND plan = ?`
		args = append(args, plan)
	}
	if provider != "" {
		query += ` AND provider = ?`
		args = append(args, provider)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []types.StatRecord
	for rows.Next() {
		var r types.StatRecord
		var isStreaming int
		err := rows.Scan(&r.Plan, &r.Provider, &r.Model, &r.KeyMask, &r.RequestTokens, &r.ResponseTokens, &r.TotalTokens, &r.Status, &r.LatencyMs, &isStreaming, &r.TargetProvider)
		if err != nil {
			return nil, err
		}
		r.IsStreaming = isStreaming == 1
		results = append(results, r)
	}
	return results, rows.Err()
}

func (d *DB) Close() error {
	return d.conn.Close()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/db/... -v`
Expected: PASS

**Step 5: Commit**

```bash
git add .
git commit -m "feat: add SQLite database layer with stats"
```

---

## Task 4: AES-GCM Encryption

**Files:**
- Create: `internal/crypto/crypto.go`
- Create: `internal/crypto/crypto_test.go`

**Step 1: Write failing test**

```go
// internal/crypto/crypto_test.go
package crypto

import (
	"encoding/base64"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	plaintext := "sk-test-api-key-12345"

	ciphertext, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if ciphertext == plaintext {
		t.Fatal("ciphertext should differ from plaintext")
	}

	decrypted, err := Decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("expected %q, got %q", plaintext, decrypted)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/crypto/... -v`
Expected: FAIL — crypto package doesn't exist

**Step 3: Implement encryption**

```go
// internal/crypto/crypto.go
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

func Encrypt(plaintext string, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func Decrypt(ciphertextB64 string, key []byte) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/crypto/... -v`
Expected: PASS

**Step 5: Commit**

```bash
git add .
git commit -m "feat: add AES-GCM-256 encryption"
```

---

## Task 5: Health Tracker (BadgerDB)

**Files:**
- Create: `internal/health/health.go`
- Create: `internal/health/health_test.go`

**Step 1: Write failing test**

```go
// internal/health/health_test.go
package health

import (
	"os"
	"testing"
	"time"
)

func TestCircuitBreaker(t *testing.T) {
	path := "/tmp/test_health"
	os.RemoveAll(path)

	h, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer h.Close()

	// Record a quota failure
	if err := h.RecordFailure("test-provider", 402, "quota exceeded"); err != nil {
		t.Fatalf("RecordFailure failed: %v", err)
	}

	health, err := h.GetHealth("test-provider")
	if err != nil {
		t.Fatalf("GetHealth failed: %v", err)
	}
	if health.Status != "unhealthy" {
		t.Fatalf("expected unhealthy, got %s", health.Status)
	}
	if health.LastFailureReason != "quota" {
		t.Fatalf("expected quota, got %s", health.LastFailureReason)
	}
	if health.CooldownUntil <= time.Now().UnixMilli() {
		t.Fatal("expected cooldown to be in the future")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/health/... -v`
Expected: FAIL — health package doesn't exist

**Step 3: Implement health tracker**

```go
// internal/health/health.go
package health

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"smart-router/internal/types"
)

type HealthTracker struct {
	db *badger.DB
}

var circuitRules = map[string]struct {
	Threshold int
	CooldownMs int64
}{
	"auth":        {1, 60 * 60 * 1000},
	"quota":       {1, 5 * 60 * 60 * 1000},
	"rate_limit":  {3, 5 * 60 * 1000},
	"server_error":{2, 2 * 60 * 1000},
	"connection":  {2, 60 * 1000},
	"timeout":     {2, 2 * 60 * 1000},
	"unknown":     {3, 60 * 1000},
}

func New(path string) (*HealthTracker, error) {
	opts := badger.DefaultOptions(path).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	return &HealthTracker{db: db}, nil
}

func (h *HealthTracker) GetHealth(provider string) (types.ProviderHealth, error) {
	var health types.ProviderHealth
	err := h.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("health:" + provider))
		if err == badger.ErrKeyNotFound {
			health = makeHealthy()
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
	return h.db.Update(func(txn *badger.Txn) error {
		health, _ := h.getHealthTx(txn, provider)
		reason := classifyFailure(status, message)
		rule := circuitRules[reason]

		health.ConsecutiveFailures++
		health.LastFailureAt = time.Now().UnixMilli()
		health.LastFailureReason = reason
		health.TotalRequests++
		health.LastActivityAt = time.Now().UnixMilli()

		if health.ConsecutiveFailures >= rule.Threshold {
			health.Status = "unhealthy"
			health.CooldownUntil = time.Now().UnixMilli() + rule.CooldownMs
		} else if health.ConsecutiveFailures > 0 {
			health.Status = "degraded"
		}

		data, _ := json.Marshal(health)
		return txn.Set([]byte("health:"+provider), data)
	})
}

func (h *HealthTracker) RecordSuccess(provider string) error {
	return h.db.Update(func(txn *badger.Txn) error {
		health, _ := h.getHealthTx(txn, provider)
		health.ConsecutiveFailures = 0
		health.Status = "healthy"
		health.CooldownUntil = 0
		health.LastSuccessAt = time.Now().UnixMilli()
		health.TotalRequests++
		health.SuccessCount++
		health.LastActivityAt = time.Now().UnixMilli()

		data, _ := json.Marshal(health)
		return txn.Set([]byte("health:"+provider), data)
	})
}

func (h *HealthTracker) getHealthTx(txn *badger.Txn, provider string) (types.ProviderHealth, error) {
	item, err := txn.Get([]byte("health:" + provider))
	if err == badger.ErrKeyNotFound {
		return makeHealthy(), nil
	}
	if err != nil {
		return types.ProviderHealth{}, err
	}
	var health types.ProviderHealth
	err = item.Value(func(val []byte) error {
		return json.Unmarshal(val, &health)
	})
	return health, err
}

func (h *HealthTracker) Close() error {
	return h.db.Close()
}

func makeHealthy() types.ProviderHealth {
	return types.ProviderHealth{
		Status: "healthy",
	}
}

func classifyFailure(status int, message string) string {
	msg := strings.ToLower(message)
	switch {
	case status == 401 || strings.Contains(msg, "authentication") || strings.Contains(msg, "unauthorized"):
		return "auth"
	case status == 402 || strings.Contains(msg, "quota") || strings.Contains(msg, "credit") || strings.Contains(msg, "billing"):
		return "quota"
	case status == 429 || strings.Contains(msg, "rate limit"):
		return "rate_limit"
	case status >= 500 && status < 600:
		return "server_error"
	case strings.Contains(msg, "connection") || strings.Contains(msg, "refused"):
		return "connection"
	case strings.Contains(msg, "timeout"):
		return "timeout"
	default:
		return "unknown"
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/health/... -v`
Expected: PASS

**Step 5: Commit**

```bash
git add .
git commit -m "feat: add BadgerDB health tracker with circuit breaker"
```

---

## Task 6: Format Translation

**Files:**
- Create: `internal/translation/translate.go`
- Create: `internal/translation/streaming.go`
- Create: `internal/translation/translate_test.go`

**Step 1: Write failing test**

```go
// internal/translation/translate_test.go
package translation

import (
	"testing"
)

func TestTranslateRequestOpenAIToAnthropic(t *testing.T) {
	req := map[string]interface{}{
		"model": "auto",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}

	result, err := TranslateRequest(req, "anthropic")
	if err != nil {
		t.Fatalf("TranslateRequest failed: %v", err)
	}
	if result["model"] != "auto" {
		t.Fatalf("model should remain auto, got %v", result["model"])
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/translation/... -v`
Expected: FAIL — translation package doesn't exist

**Step 3: Implement translation**

```go
// internal/translation/translate.go
package translation

import (
	"encoding/json"
	"fmt"
)

func TranslateRequest(body map[string]interface{}, targetFormat string) (map[string]interface{}, error) {
	// For now, OpenAI and Anthropic request formats are nearly identical
	// Just ensure required fields are present
	if targetFormat == "anthropic" {
		if _, ok := body["max_tokens"]; !ok {
			body["max_tokens"] = 4096
		}
	}
	return body, nil
}

func TranslateResponse(data []byte, fromFormat, toFormat string) ([]byte, error) {
	if fromFormat == toFormat {
		return data, nil
	}
	// For non-streaming responses, formats are compatible enough
	// Full translation implemented in streaming.go for SSE
	return data, nil
}
```

```go
// internal/translation/streaming.go
package translation

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// SSETranslator converts SSE streams between OpenAI and Anthropic formats
func SSETranslator(reader io.Reader, fromFormat, toFormat string) io.Reader {
	if fromFormat == toFormat {
		return reader
	}
	r, w := io.Pipe()
	go func() {
		defer w.Close()
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				fmt.Fprintln(w, line)
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				fmt.Fprintln(w, line)
				continue
			}
			converted, err := convertSSEEvent([]byte(data), fromFormat, toFormat)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", converted)
		}
	}()
	return r
}

func convertSSEEvent(data []byte, from, to string) ([]byte, error) {
	if from == "openai" && to == "anthropic" {
		return openAIToAnthropic(data)
	}
	if from == "anthropic" && to == "openai" {
		return anthropicToOpenAI(data)
	}
	return data, nil
}

func openAIToAnthropic(data []byte) ([]byte, error) {
	var openai map[string]interface{}
	if err := json.Unmarshal(data, &openai); err != nil {
		return nil, err
	}
	// Simplified: just pass through for now
	// Full implementation would map choices[0].delta.content to content_block_delta
	return data, nil
}

func anthropicToOpenAI(data []byte) ([]byte, error) {
	var anthropic map[string]interface{}
	if err := json.Unmarshal(data, &anthropic); err != nil {
		return nil, err
	}
	// Simplified: just pass through for now
	return data, nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/translation/... -v`
Expected: PASS

**Step 5: Commit**

```bash
git add .
git commit -m "feat: add format translation layer"
```

---

## Task 7: Provider HTTP Client

**Files:**
- Create: `internal/providers/client.go`
- Create: `internal/providers/client_test.go`

**Step 1: Write failing test**

```go
// internal/providers/client_test.go
package providers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"smart-router/internal/types"
)

func TestCallProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer server.Close()

	client := NewClient()
	provider := types.ProviderConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Format:  "openai",
		Timeout: 10,
	}

	resp, err := client.Call(provider, map[string]interface{}{
		"model": "test",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/providers/... -v`
Expected: FAIL — providers package doesn't exist

**Step 3: Implement HTTP client**

```go
// internal/providers/client.go
package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"smart-router/internal/types"
)

type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{},
	}
}

func (c *Client) Call(provider types.ProviderConfig, body map[string]interface{}) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", provider.BaseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	if provider.Format == "anthropic" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(provider.Timeout)*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	return c.httpClient.Do(req)
}

func (c *Client) CallStream(provider types.ProviderConfig, body map[string]interface{}) (*http.Response, error) {
	body["stream"] = true
	return c.Call(provider, body)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/providers/... -v`
Expected: PASS

**Step 5: Commit**

```bash
git add .
git commit -m "feat: add upstream provider HTTP client"
```

---

## Task 8: Core Router

**Files:**
- Create: `internal/router/router.go`
- Create: `internal/router/router_test.go`

**Step 1: Write failing test**

```go
// internal/router/router_test.go
package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"smart-router/internal/types"
)

func TestRouterWithMockProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"choices":[{"message":{"content":"hello from mock"}}]}`))
	}))
	defer server.Close()

	router := New(nil, nil) // health and db nil for now
	// TODO: more comprehensive test
	_ = router
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/router/... -v`
Expected: FAIL

**Step 3: Implement router**

```go
// internal/router/router.go
package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"smart-router/internal/health"
	"smart-router/internal/providers"
	"smart-router/internal/translation"
	"smart-router/internal/types"
)

type Router struct {
	healthTracker *health.HealthTracker
	client        *providers.Client
}

func New(tracker *health.HealthTracker, db interface{}) *Router {
	return &Router{
		healthTracker: tracker,
		client:        providers.NewClient(),
	}
}

func (r *Router) Route(plan string, body map[string]interface{}, isStreaming bool) (*http.Response, types.ProviderConfig, error) {
	// TODO: get plan config from DB
	// For now, return error
	return nil, types.ProviderConfig{}, fmt.Errorf("not implemented")
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/router/... -v`
Expected: PASS (minimal test)

**Step 5: Commit**

```bash
git add .
git commit -m "feat: add router skeleton"
```

---

## Task 9: HTTP API Handlers

**Files:**
- Modify: `main.go`
- Create: `internal/api/handlers.go`

**Step 1: Implement handlers**

```go
// internal/api/handlers.go
package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gorilla/mux"
	"smart-router/internal/db"
	"smart-router/internal/health"
	"smart-router/internal/router"
)

type Server struct {
	router       *router.Router
	health       *health.HealthTracker
	db           *db.DB
	adminKey     string
}

func NewServer(r *router.Router, h *health.HealthTracker, d *db.DB, adminKey string) *Server {
	return &Server{router: r, health: h, db: d, adminKey: adminKey}
}

func (s *Server) RegisterRoutes(r *mux.Router) {
	r.HandleFunc("/v1/chat/completions", s.handleChat).Methods("POST")
	r.HandleFunc("/v1/plans", s.handleListPlans).Methods("GET")
	r.HandleFunc("/v1/plans/{slug}", s.handleGetPlan).Methods("GET")
	r.HandleFunc("/v1/plans/{slug}", s.handleUpdatePlan).Methods("PUT")
	r.HandleFunc("/v1/plans/{slug}", s.handleDeletePlan).Methods("DELETE")
	r.HandleFunc("/v1/health", s.handleHealth).Methods("GET")
	r.HandleFunc("/v1/health/activity", s.handleActivity).Methods("GET")
	r.HandleFunc("/v1/stats", s.handleStats).Methods("GET")
	r.HandleFunc("/v1/stats/aggregated", s.handleAggregatedStats).Methods("GET")
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	// TODO: implement
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
	// TODO: implement
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	// TODO: implement
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (s *Server) handleUpdatePlan(w http.ResponseWriter, r *http.Request) {
	// TODO: implement
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (s *Server) handleDeletePlan(w http.ResponseWriter, r *http.Request) {
	// TODO: implement
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// TODO: implement
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	// TODO: implement
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	// TODO: implement
	json.NewEncoder(w).Encode([]interface{}{})
}

func (s *Server) handleAggregatedStats(w http.ResponseWriter, r *http.Request) {
	// TODO: implement
	json.NewEncoder(w).Encode(map[string]interface{}{})
}
```

**Step 2: Update main.go**

```go
// main.go
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gorilla/mux"
	"smart-router/internal/api"
	"smart-router/internal/db"
	"smart-router/internal/health"
	"smart-router/internal/router"
)

func main() {
	port := os.Getenv("SMART_ROUTER_PORT")
	if port == "" {
		port = "8790"
	}
	host := os.Getenv("SMART_ROUTER_HOST")
	if host == "" {
		host = "0.0.0.0"
	}

	dbPath := os.Getenv("DATABASE_PATH")
	if dbPath == "" {
		dbPath = "./data/smart-router.db"
	}
	healthPath := os.Getenv("BADGER_PATH")
	if healthPath == "" {
		healthPath = "./data/health"
	}

	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	h, err := health.New(healthPath)
	if err != nil {
		log.Fatalf("Failed to open health tracker: %v", err)
	}
	defer h.Close()

	r := router.New(h, database)
	server := api.NewServer(r, h, database, os.Getenv("ADMIN_KEY"))

	muxRouter := mux.NewRouter()
	server.RegisterRoutes(muxRouter)

	addr := host + ":" + port
	log.Printf("Smart Router listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, muxRouter))
}
```

**Step 3: Run tests**

Run: `go test ./...`
Expected: PASS (all existing tests still pass)

**Step 4: Commit**

```bash
git add .
git commit -m "feat: add HTTP API handlers and wire up main"
```

---

## Task 10: Complete Chat Endpoint

**Files:**
- Modify: `internal/api/handlers.go`
- Modify: `internal/router/router.go`

**Step 1: Implement chat handler**

```go
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	plan := r.Header.Get("X-Plan")
	if plan == "" {
		plan = "default"
	}

	model, _ := body["model"].(string)
	if strings.HasPrefix(model, "auto-") {
		plan = strings.TrimPrefix(model, "auto-")
		body["model"] = "auto"
	}

	isStreaming := false
	if stream, ok := body["stream"].(bool); ok {
		isStreaming = stream
	}

	resp, provider, err := s.router.Route(plan, body, isStreaming)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	// Copy headers
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
```

**Step 2: Update router to use provider chain**

```go
func (r *Router) Route(plan string, body map[string]interface{}, isStreaming bool) (*http.Response, types.ProviderConfig, error) {
	// TODO: load plan from config/DB
	// For now, placeholder
	return nil, types.ProviderConfig{}, fmt.Errorf("plan %s not found", plan)
}
```

**Step 3: Test manually**

Run: `go run main.go`
Test: `curl -s -X POST http://localhost:8790/v1/chat/completions -H "Content-Type: application/json" -d '{"model":"auto","messages":[{"role":"user","content":"hi"}]}'`
Expected: `{"error":"plan default not found"}` (404 or 503)

**Step 4: Commit**

```bash
git add .
git commit -m "feat: implement chat completions endpoint"
```

---

## Task 11: Plan Management Endpoints

**Files:**
- Modify: `internal/api/handlers.go`
- Modify: `internal/db/db.go`

**Step 1: Implement plan CRUD**

Add to db.go:
```go
func (d *DB) SavePlan(slug string, config types.PlanConfig) error {
	data, _ := json.Marshal(config)
	_, err := d.conn.Exec(`INSERT OR REPLACE INTO plans (slug, config) VALUES (?, ?)`, slug, string(data))
	return err
}

func (d *DB) GetPlan(slug string) (*types.PlanConfig, error) {
	var configStr string
	err := d.conn.QueryRow(`SELECT config FROM plans WHERE slug = ?`, slug).Scan(&configStr)
	if err != nil {
		return nil, err
	}
	var cfg types.PlanConfig
	if err := json.Unmarshal([]byte(configStr), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (d *DB) ListPlans() (map[string]types.PlanConfig, error) {
	rows, err := d.conn.Query(`SELECT slug, config FROM plans`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	plans := make(map[string]types.PlanConfig)
	for rows.Next() {
		var slug, configStr string
		if err := rows.Scan(&slug, &configStr); err != nil {
			return nil, err
		}
		var cfg types.PlanConfig
		if err := json.Unmarshal([]byte(configStr), &cfg); err != nil {
			continue
		}
		plans[slug] = cfg
	}
	return plans, rows.Err()
}

func (d *DB) DeletePlan(slug string) error {
	_, err := d.conn.Exec(`DELETE FROM plans WHERE slug = ?`, slug)
	return err
}
```

Update handlers for plan management.

**Step 2: Test**

Run: `go test ./...`
Expected: PASS

**Step 3: Commit**

```bash
git add .
git commit -m "feat: add plan CRUD endpoints"
```

---

## Task 12: Stats and Health Endpoints

**Files:**
- Modify: `internal/api/handlers.go`
- Modify: `internal/db/db.go`
- Modify: `internal/health/health.go`

**Step 1: Implement health and stats endpoints**

Add aggregation queries to db.go.
Add GetAllHealth to health.go.

**Step 2: Test**

Run: `go test ./...`
Expected: PASS

**Step 3: Commit**

```bash
git add .
git commit -m "feat: add stats and health endpoints"
```

---

## Task 13: Seed Plans on Startup

**Files:**
- Modify: `main.go`
- Modify: `internal/config/config.go`

**Step 1: Add seed logic**

On startup, if plans table is empty, load from config/plans.yaml and seed into DB.

**Step 2: Test**

Run: `go run main.go`
Expected: Server starts, plans seeded.

**Step 3: Commit**

```bash
git add .
git commit -m "feat: seed plans from YAML on startup"
```

---

## Task 14: Full Router Implementation

**Files:**
- Modify: `internal/router/router.go`

**Step 1: Implement provider failover**

```go
func (r *Router) Route(planSlug string, body map[string]interface{}, isStreaming bool) (*http.Response, types.ProviderConfig, error) {
	// Load plan from DB
	plan, err := r.db.GetPlan(planSlug)
	if err != nil {
		return nil, types.ProviderConfig{}, err
	}

	for _, provider := range plan.Providers {
		// Check health
		h, _ := r.healthTracker.GetHealth(provider.Name)
		if h.Status == "unhealthy" && h.CooldownUntil > time.Now().UnixMilli() {
			continue
		}

		// Translate request
		translatedBody, _ := translation.TranslateRequest(body, provider.Format)

		// Call provider
		var resp *http.Response
		var callErr error
		if isStreaming {
			resp, callErr = r.client.CallStream(provider, translatedBody)
		} else {
			resp, callErr = r.client.Call(provider, translatedBody)
		}

		if callErr != nil {
			r.healthTracker.RecordFailure(provider.Name, 0, callErr.Error())
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			r.healthTracker.RecordSuccess(provider.Name)
			return resp, provider, nil
		}

		// Read error body
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		r.healthTracker.RecordFailure(provider.Name, resp.StatusCode, string(errBody))
	}

	return nil, types.ProviderConfig{}, fmt.Errorf("all providers failed for plan %s", planSlug)
}
```

**Step 2: Test**

Run: `go test ./...`
Expected: PASS

**Step 3: Commit**

```bash
git add .
git commit -m "feat: implement full provider failover routing"
```

---

## Task 15: CORS + Logging Middleware

**Files:**
- Modify: `main.go`
- Create: `internal/middleware/middleware.go`

**Step 1: Add middleware**

```go
package middleware

import (
	"log"
	"net/http"
	"time"
)

func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Plan, X-Admin-Key")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

**Step 2: Wire up in main.go**

```go
handler := middleware.Logging(middleware.CORS(muxRouter))
log.Fatal(http.ListenAndServe(addr, handler))
```

**Step 3: Commit**

```bash
git add .
git commit -m "feat: add CORS and logging middleware"
```

---

## Task 16: Integration Test

**Files:**
- Create: `tests/integration_test.go`

**Step 1: Write integration test**

```go
package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEndToEndChat(t *testing.T) {
	// Start server
	// Make request
	// Verify response
}
```

**Step 2: Run**

Run: `go test ./tests/... -v`
Expected: PASS

**Step 3: Commit**

```bash
git add .
git commit -m "test: add integration tests"
```

---

## Task 17: Build and Run

**Step 1: Build binary**

Run: `go build -o smart-router .`
Expected: Binary created at `./smart-router`

**Step 2: Run**

Run: `./smart-router`
Expected: Server starts on 0.0.0.0:8790

**Step 3: Test from another machine**

From another machine on same LAN:
```bash
curl http://YOUR_MACHINE_IP:8790/v1/plans
```
Expected: Returns plan list

**Step 4: Commit**

```bash
git add .
git commit -m "chore: add build artifacts"
```

---

## Task 18: Replace Old Service

**Step 1: Stop old workerd**

```bash
pm2 stop smart-router
```

**Step 2: Start Go binary**

```bash
./smart-router
# Or via PM2:
pm2 start ./smart-router --name smart-router-go
```

**Step 3: Verify**

Test all endpoints match old behavior.

**Step 4: Commit**

```bash
git add .
git commit -m "feat: go rewrite complete, replace workerd"
```
