package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
	"smart-router/internal/types"
)

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS request_stats (
		id INTEGER PRIMARY KEY,
		plan TEXT NOT NULL,
		provider TEXT NOT NULL,
		model TEXT NOT NULL,
		key_mask TEXT,
		client_key TEXT,
		request_tokens INTEGER NOT NULL DEFAULT 0,
		response_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL,
		latency_ms INTEGER NOT NULL,
		is_streaming INTEGER NOT NULL DEFAULT 0,
		target_provider TEXT,
		created_at INTEGER NOT NULL DEFAULT (strftime('%s','now') * 1000)
	);`,
	`CREATE INDEX IF NOT EXISTS idx_stats_plan ON request_stats(plan);`,
	`CREATE INDEX IF NOT EXISTS idx_stats_provider ON request_stats(provider);`,
	`CREATE INDEX IF NOT EXISTS idx_stats_created ON request_stats(created_at);`,
	`ALTER TABLE request_stats ADD COLUMN client_key TEXT;`,
	`ALTER TABLE request_stats ADD COLUMN key_mask TEXT;`,
	`ALTER TABLE request_stats ADD COLUMN target_provider TEXT;`,
	`ALTER TABLE request_stats ADD COLUMN status_code INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE request_stats ADD COLUMN error_reason TEXT;`,
	`ALTER TABLE request_stats ADD COLUMN source TEXT;`,
	`ALTER TABLE request_stats ADD COLUMN user_agent TEXT;`,
	// client_key column stores the MASKED form (e.g. "****abcd"), not the
	// raw API key. Despite the column name, all INSERTs apply MaskAPIKey.
	`CREATE INDEX IF NOT EXISTS idx_stats_client_key ON request_stats(client_key);`,
	`CREATE INDEX IF NOT EXISTS idx_stats_client_created ON request_stats(client_key, created_at);`,
	`CREATE INDEX IF NOT EXISTS idx_stats_source ON request_stats(source);`,
	`CREATE TABLE IF NOT EXISTS plans (
		slug TEXT PRIMARY KEY,
		config TEXT NOT NULL
	);`,
	`CREATE TABLE IF NOT EXISTS api_keys (
		key TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		plans TEXT NOT NULL DEFAULT '[]',
		models TEXT NOT NULL DEFAULT '[]',
		allowed_ips TEXT NOT NULL DEFAULT '[]',
		rate_limit_rpm INTEGER NOT NULL DEFAULT 0,
		rate_limit_rpd INTEGER NOT NULL DEFAULT 0,
		monthly_token_limit INTEGER NOT NULL DEFAULT 0,
		monthly_request_limit INTEGER NOT NULL DEFAULT 0,
		expires_at INTEGER,
		disabled INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
		last_used_at INTEGER
	);`,
	`CREATE INDEX IF NOT EXISTS idx_api_keys_disabled ON api_keys(disabled);`,
	`CREATE INDEX IF NOT EXISTS idx_api_keys_name ON api_keys(name);`,
	`ALTER TABLE api_keys ADD COLUMN webhook_url TEXT;`,
	`ALTER TABLE api_keys ADD COLUMN group_id INTEGER;`,
	`CREATE TABLE IF NOT EXISTS audit_log (
		id INTEGER PRIMARY KEY,
		action TEXT NOT NULL,
		target_key TEXT,
		actor TEXT,
		details TEXT,
		created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);`,
	`CREATE TABLE IF NOT EXISTS model_pricing (
		model TEXT PRIMARY KEY,
		input_price_per_1k REAL NOT NULL DEFAULT 0,
		output_price_per_1k REAL NOT NULL DEFAULT 0
	);`,
	`CREATE TABLE IF NOT EXISTS key_groups (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		monthly_token_limit INTEGER NOT NULL DEFAULT 0,
		monthly_request_limit INTEGER NOT NULL DEFAULT 0,
		monthly_budget_limit REAL NOT NULL DEFAULT 0,
		webhook_url TEXT
	);`,
}

type DB struct {
	conn              *sql.DB
	statChan          chan types.StatRecord
	stopChan          chan struct{}
	wg                sync.WaitGroup
	encKey            []byte
	pricingCache      map[string][2]float64
	pricingCacheMu    sync.RWMutex
	pricingCacheUntil time.Time
}

// ExtractSource classifies the request source from the User-Agent header.
// Returns a short, human-readable label like "claude-code", "hermes", "openai", etc.
func ExtractSource(userAgent string) string {
	ua := strings.ToLower(userAgent)
	switch {
	case strings.Contains(ua, "claude-cli"), strings.Contains(ua, "claude-code"):
		return "claude-code"
	case strings.Contains(ua, "hermes"):
		return "hermes"
	case strings.Contains(ua, "openai"):
		return "openai"
	case strings.Contains(ua, "anthropic"):
		return "anthropic"
	case strings.Contains(ua, "curl"):
		return "curl"
	case strings.Contains(ua, "python-httpx"), strings.Contains(ua, "python-requests"), strings.Contains(ua, "aiohttp"):
		return "python"
	case strings.Contains(ua, "node"), strings.Contains(ua, "axios"):
		return "node"
	case ua == "":
		return "unknown"
	default:
		return "other"
	}
}

func (d *DB) WithEncryptionKey(key []byte) *DB {
	d.encKey = key
	return d
}

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	if _, err := conn.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("enable wal: %w", err)
	}
	if _, err := conn.Exec(`PRAGMA busy_timeout=5000;`); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}

	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(time.Hour)

	db := &DB{
		conn:     conn,
		statChan: make(chan types.StatRecord, 1000),
		stopChan: make(chan struct{}),
	}
	if err := db.migrate(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	db.startStatWorker()
	db.startCleanupWorker()
	return db, nil
}

func (d *DB) migrate() error {
	for _, stmt := range migrations {
		if _, err := d.conn.Exec(stmt); err != nil {
			// Ignore "duplicate column name" errors from ALTER TABLE ADD COLUMN
			// so migrations are idempotent on existing databases.
			if strings.HasPrefix(strings.ToUpper(stmt), "ALTER TABLE") &&
				strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return err
		}
	}
	return nil
}

func (d *DB) RecordStat(r types.StatRecord) error {
	return d.RecordStatBatch([]types.StatRecord{r})
}

func (d *DB) RecordStatBatch(records []types.StatRecord) error {
	if len(records) == 0 {
		return nil
	}

	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin batch tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO request_stats
			(plan, provider, model, key_mask, client_key, source, user_agent, request_tokens, response_tokens, total_tokens, status, status_code, error_reason, latency_ms, is_streaming, target_provider)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare batch stmt: %w", err)
	}
	defer stmt.Close()

	for _, r := range records {
		streaming := 0
		if r.IsStreaming {
			streaming = 1
		}
		if _, err := stmt.Exec(
			r.Plan, r.Provider, r.Model, r.KeyMask, r.ClientKey, r.Source, r.UserAgent,
			r.RequestTokens, r.ResponseTokens, r.TotalTokens,
			r.Status, r.StatusCode, r.ErrorReason, r.LatencyMs, streaming, r.TargetProvider,
		); err != nil {
			return fmt.Errorf("exec batch insert: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch tx: %w", err)
	}
	return nil
}

func (d *DB) startStatWorker() {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		const batchSize = 50
		batch := make([]types.StatRecord, 0, batchSize)

		flush := func() {
			if len(batch) == 0 {
				return
			}
			if err := d.RecordStatBatch(batch); err != nil {
				log.Printf("[DB] batch stat insert failed: %v", err)
			}
			batch = batch[:0]
		}

	outer:
		for {
				select {
				case r := <-d.statChan:
					batch = append(batch, r)
					// Non-blocking drain to fill batch
					for len(batch) < batchSize {
						select {
						case r2 := <-d.statChan:
							batch = append(batch, r2)
						default:
							flush()
							continue outer
						}
					}
					flush()
				case <-d.stopChan:
				// Drain remaining stats before exiting
				for {
					select {
					case r := <-d.statChan:
						batch = append(batch, r)
					default:
						flush()
						return
					}
				}
			}
		}
	}()
}

func (d *DB) startCleanupWorker() {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Keep last 90 days of stats
				cutoff := time.Now().Add(-90 * 24 * time.Hour).UnixMilli()
				if _, err := d.conn.Exec(`DELETE FROM request_stats WHERE created_at < ?`, cutoff); err != nil {
					log.Printf("[DB] stats cleanup failed: %v", err)
				}
				if _, err := d.conn.Exec(`PRAGMA wal_checkpoint(PASSIVE);`); err != nil {
					log.Printf("[DB] WAL checkpoint failed: %v", err)
				}
			case <-d.stopChan:
				return
			}
		}
	}()
}

// RecordStatAsync sends a stat record to the background worker.
// If the channel is full, it falls back to synchronous insertion.
// During shutdown, stats are silently dropped to avoid panicking on a closed channel.
func (d *DB) RecordStatAsync(r types.StatRecord) {
	select {
	case <-d.stopChan:
		return
	case d.statChan <- r:
	default:
		// Channel full — write synchronously so we don't drop stats
		if err := d.RecordStat(r); err != nil {
			log.Printf("[DB] sync stat insert failed: %v", err)
		}
	}
}

func (d *DB) GetStats(plan, provider string, limit int) ([]types.StatRecord, error) {
	const maxLimit = 10000
	if limit <= 0 || limit > maxLimit {
		limit = maxLimit
	}
	query := `
		SELECT plan, provider, model, key_mask, client_key, source, user_agent, request_tokens, response_tokens, total_tokens, status, status_code, error_reason, latency_ms, is_streaming, target_provider
		FROM request_stats
		WHERE 1=1`
	args := []any{}

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
		return nil, fmt.Errorf("query stats: %w", err)
	}
	defer rows.Close()

	var results []types.StatRecord
	for rows.Next() {
		var r types.StatRecord
		var streaming int
		var source sql.NullString
		var userAgent sql.NullString
		err := rows.Scan(
			&r.Plan,
			&r.Provider,
			&r.Model,
			&r.KeyMask,
			&r.ClientKey,
			&source,
			&userAgent,
			&r.RequestTokens,
			&r.ResponseTokens,
			&r.TotalTokens,
			&r.Status,
			&r.StatusCode,
			&r.ErrorReason,
			&r.LatencyMs,
			&streaming,
			&r.TargetProvider,
		)
		if err != nil {
			return nil, fmt.Errorf("scan stat: %w", err)
		}
		if source.Valid {
			r.Source = source.String
		}
		if userAgent.Valid {
			r.UserAgent = userAgent.String
		}
		r.IsStreaming = streaming != 0
		results = append(results, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return results, nil
}

func (d *DB) SavePlan(slug string, config types.PlanConfig) error {
	// Deep-copy and encrypt provider API keys before saving.
	if len(d.encKey) > 0 {
		config = clonePlanConfig(config)
		for i := range config.Providers {
			if config.Providers[i].APIKey != "" {
				enc, err := encryptValue(d.encKey, config.Providers[i].APIKey)
				if err != nil {
					return fmt.Errorf("encrypt api_key for provider %s: %w", config.Providers[i].Name, err)
				}
				config.Providers[i].APIKey = enc
			}
		}
	}

	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal plan config: %w", err)
	}

	_, err = d.conn.Exec(`
		INSERT INTO plans (slug, config)
		VALUES (?, ?)
		ON CONFLICT(slug) DO UPDATE SET config = excluded.config
	`, slug, string(data))

	if err != nil {
		return fmt.Errorf("save plan: %w", err)
	}
	return nil
}

func clonePlanConfig(config types.PlanConfig) types.PlanConfig {
	cloned := types.PlanConfig{
		Strategy:  config.Strategy,
		Providers: make([]types.ProviderConfig, len(config.Providers)),
	}
	copy(cloned.Providers, config.Providers)
	return cloned
}

func (d *DB) GetPlan(slug string) (*types.PlanConfig, error) {
	var configJSON string
	err := d.conn.QueryRow(`SELECT config FROM plans WHERE slug = ?`, slug).Scan(&configJSON)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("plan not found: %s", slug)
	}
	if err != nil {
		return nil, fmt.Errorf("get plan: %w", err)
	}

	var config types.PlanConfig
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, fmt.Errorf("unmarshal plan config: %w", err)
	}
	if err := decryptPlanProviders(d.encKey, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func (d *DB) ListPlans() (map[string]types.PlanConfig, error) {
	rows, err := d.conn.Query(`SELECT slug, config FROM plans`)
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	defer rows.Close()

	plans := make(map[string]types.PlanConfig)
	for rows.Next() {
		var slug string
		var configJSON string
		if err := rows.Scan(&slug, &configJSON); err != nil {
			return nil, fmt.Errorf("scan plan: %w", err)
		}
		var config types.PlanConfig
		if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
			return nil, fmt.Errorf("unmarshal plan config: %w", err)
		}
		if err := decryptPlanProviders(d.encKey, &config); err != nil {
			return nil, err
		}
		plans[slug] = config
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return plans, nil
}

func decryptPlanProviders(key []byte, config *types.PlanConfig) error {
	if key == nil {
		return nil
	}
	for i := range config.Providers {
		if config.Providers[i].APIKey == "" {
			continue
		}
		decrypted, err := decryptValue(key, config.Providers[i].APIKey)
		if err != nil {
			return fmt.Errorf("decrypt api_key for provider %s: %w", config.Providers[i].Name, err)
		}
		config.Providers[i].APIKey = decrypted
	}
	return nil
}

func (d *DB) DeletePlan(slug string) error {
	res, err := d.conn.Exec(`DELETE FROM plans WHERE slug = ?`, slug)
	if err != nil {
		return fmt.Errorf("delete plan: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("plan not found")
	}
	return nil
}

func (d *DB) Close() error {
	close(d.stopChan)
	d.wg.Wait()
	return d.conn.Close()
}

// FlushStats drains the async stat channel, writing any pending records
// synchronously, then pauses briefly to let the background worker finish
// any in-flight insert. Intended for use in tests.
func (d *DB) FlushStats() {
	batch := make([]types.StatRecord, 0, 50)
	for {
		select {
		case r := <-d.statChan:
			batch = append(batch, r)
		default:
			if len(batch) > 0 {
				if err := d.RecordStatBatch(batch); err != nil {
					log.Printf("[DB] flush stat insert failed: %v", err)
				}
			}
			time.Sleep(50 * time.Millisecond)
			return
		}
	}
}

type WeeklyUsage struct {
	RequestTokens  int64 `json:"request_tokens"`
	ResponseTokens int64 `json:"response_tokens"`
	RequestCount   int64 `json:"request_count"`
}

// GetUsageSince returns token and request counts for a provider/key_mask since the given time.
func (d *DB) GetUsageSince(keyMask string, since time.Time) (*WeeklyUsage, error) {
	var reqTokens, respTokens, reqCount sql.NullInt64
	err := d.conn.QueryRow(`
		SELECT COALESCE(SUM(request_tokens), 0), COALESCE(SUM(response_tokens), 0), COALESCE(COUNT(*), 0)
		FROM request_stats
		WHERE key_mask = ? AND created_at > ? AND status = 'success'
	`, keyMask, since.UnixMilli()).Scan(&reqTokens, &respTokens, &reqCount)

	if err != nil {
		return nil, fmt.Errorf("query usage: %w", err)
	}

	return &WeeklyUsage{
		RequestTokens:  reqTokens.Int64,
		ResponseTokens: respTokens.Int64,
		RequestCount:   reqCount.Int64,
	}, nil
}

// GetUsageSinceForPlan returns token and request counts for a provider/key_mask within a specific plan since the given time.
func (d *DB) GetUsageSinceForPlan(plan, keyMask string, since time.Time) (*WeeklyUsage, error) {
	var reqTokens, respTokens, reqCount sql.NullInt64
	err := d.conn.QueryRow(`
		SELECT COALESCE(SUM(request_tokens), 0), COALESCE(SUM(response_tokens), 0), COALESCE(COUNT(*), 0)
		FROM request_stats
		WHERE plan = ? AND key_mask = ? AND created_at > ? AND status = 'success'
	`, plan, keyMask, since.UnixMilli()).Scan(&reqTokens, &respTokens, &reqCount)

	if err != nil {
		return nil, fmt.Errorf("query usage: %w", err)
	}

	return &WeeklyUsage{
		RequestTokens:  reqTokens.Int64,
		ResponseTokens: respTokens.Int64,
		RequestCount:   reqCount.Int64,
	}, nil
}

// GetWeeklyUsage returns token and request counts for a provider/key_mask over the last 7 days.
func (d *DB) GetWeeklyUsage(keyMask string) (*WeeklyUsage, error) {
	return d.GetUsageSince(keyMask, time.Now().Add(-7*24*time.Hour))
}

// GetWeeklyUsageForPlan returns token and request counts for a provider/key_mask within a specific plan over the last 7 days.
func (d *DB) GetWeeklyUsageForPlan(plan, keyMask string) (*WeeklyUsage, error) {
	return d.GetUsageSinceForPlan(plan, keyMask, time.Now().Add(-7*24*time.Hour))
}

type LatencyStats struct {
	Count int
	P50   int
	P95   int
	Mean  int
	Max   int
}

// GetProviderLatencyStats returns p50/p95 latency percentiles for a provider
// from successful requests in the given time window.
func (d *DB) GetProviderLatencyStats(provider string, since time.Time) (*LatencyStats, error) {
	rows, err := d.conn.Query(`
		SELECT latency_ms FROM request_stats
		WHERE provider = ? AND status = 'success' AND created_at > ?
		ORDER BY latency_ms
	`, provider, since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("query latency: %w", err)
	}
	defer rows.Close()

	var vals []int
	var sum int64
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan latency: %w", err)
		}
		vals = append(vals, v)
		sum += int64(v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	if len(vals) == 0 {
		return &LatencyStats{}, nil
	}

	n := len(vals)
	p50 := vals[n/2]
	p95Idx := n * 95 / 100
	if p95Idx >= n {
		p95Idx = n - 1
	}
	p95 := vals[p95Idx]

	return &LatencyStats{
		Count: n,
		P50:   p50,
		P95:   p95,
		Mean:  int(sum / int64(n)),
		Max:   vals[n-1],
	}, nil
}

type MonthlyUsage struct {
	RequestTokens  int64   `json:"request_tokens"`
	ResponseTokens int64   `json:"response_tokens"`
	RequestCount   int64   `json:"request_count"`
	Cost           float64 `json:"cost"`
}

// scanAPIKey reads an api_keys row into a types.APIKey.
func scanAPIKey(rows *sql.Rows) (types.APIKey, error) {
	var k types.APIKey
	var plansJSON, modelsJSON, ipsJSON string
	var disabled int
	var lastUsed sql.NullInt64
	var expiresAt sql.NullInt64
	var groupID sql.NullInt64
	var webhookURL sql.NullString
	err := rows.Scan(
		&k.Key,
		&k.Name,
		&plansJSON,
		&modelsJSON,
		&ipsJSON,
		&k.RateLimitRPM,
		&k.RateLimitRPD,
		&k.MonthlyTokenLimit,
		&k.MonthlyRequestLimit,
		&expiresAt,
		&disabled,
		&k.CreatedAt,
		&lastUsed,
		&webhookURL,
		&groupID,
	)
	if err != nil {
		return k, err
	}
	k.Disabled = disabled != 0
	if expiresAt.Valid {
		k.ExpiresAt = &expiresAt.Int64
	}
	if lastUsed.Valid {
		k.LastUsedAt = &lastUsed.Int64
	}
	if groupID.Valid {
		k.GroupID = &groupID.Int64
	}
	if webhookURL.Valid {
		k.WebhookURL = webhookURL.String
	}
	if err := json.Unmarshal([]byte(plansJSON), &k.Plans); err != nil {
		return k, fmt.Errorf("unmarshal plans: %w", err)
	}
	if err := json.Unmarshal([]byte(modelsJSON), &k.Models); err != nil {
		return k, fmt.Errorf("unmarshal models: %w", err)
	}
	if err := json.Unmarshal([]byte(ipsJSON), &k.AllowedIPs); err != nil {
		return k, fmt.Errorf("unmarshal allowed_ips: %w", err)
	}
	return k, nil
}

func (d *DB) CountAPIKeys() (int, error) {
	var count int
	err := d.conn.QueryRow(`SELECT COUNT(*) FROM api_keys`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count api keys: %w", err)
	}
	return count, nil
}

func marshalJSON(v interface{}) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal JSON: %w", err)
	}
	return b, nil
}

func (d *DB) CreateAPIKey(key types.APIKey) error {
	plansJSON, err := marshalJSON(key.Plans)
	if err != nil {
		return fmt.Errorf("create api key: plans: %w", err)
	}
	modelsJSON, err := marshalJSON(key.Models)
	if err != nil {
		return fmt.Errorf("create api key: models: %w", err)
	}
	ipsJSON, err := marshalJSON(key.AllowedIPs)
	if err != nil {
		return fmt.Errorf("create api key: allowed_ips: %w", err)
	}
	var expiresAt interface{}
	if key.ExpiresAt != nil {
		expiresAt = *key.ExpiresAt
	}
	var groupID interface{}
	if key.GroupID != nil {
		groupID = *key.GroupID
	}
	disabled := 0
	if key.Disabled {
		disabled = 1
	}
	_, err = d.conn.Exec(`
		INSERT INTO api_keys
			(key, name, plans, models, allowed_ips, rate_limit_rpm, rate_limit_rpd, monthly_token_limit, monthly_request_limit, expires_at, disabled, created_at, webhook_url, group_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, key.Key, key.Name, string(plansJSON), string(modelsJSON), string(ipsJSON),
		key.RateLimitRPM, key.RateLimitRPD, key.MonthlyTokenLimit, key.MonthlyRequestLimit,
		expiresAt, disabled, key.CreatedAt, key.WebhookURL, groupID)
	if err != nil {
		return fmt.Errorf("create api key: %w", err)
	}
	return nil
}

func (d *DB) GetAPIKey(key string) (*types.APIKey, error) {
	rows, err := d.conn.Query(`
		SELECT key, name, plans, models, allowed_ips, rate_limit_rpm, rate_limit_rpd,
			monthly_token_limit, monthly_request_limit, expires_at, disabled, created_at, last_used_at, webhook_url, group_id
		FROM api_keys WHERE key = ?
	`, key)
	if err != nil {
		return nil, fmt.Errorf("query api key: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("query api key: %w", err)
		}
		return nil, fmt.Errorf("api key not found")
	}
	k, err := scanAPIKey(rows)
	if err != nil {
		return nil, fmt.Errorf("scan api key: %w", err)
	}
	return &k, nil
}

func (d *DB) GetAPIKeyByName(name string) (*types.APIKey, error) {
	rows, err := d.conn.Query(`
		SELECT key, name, plans, models, allowed_ips, rate_limit_rpm, rate_limit_rpd,
			monthly_token_limit, monthly_request_limit, expires_at, disabled, created_at, last_used_at, webhook_url, group_id
		FROM api_keys WHERE name = ?
	`, name)
	if err != nil {
		return nil, fmt.Errorf("query api key by name: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("query api key by name: %w", err)
		}
		return nil, fmt.Errorf("api key not found")
	}
	k, err := scanAPIKey(rows)
	if err != nil {
		return nil, fmt.Errorf("scan api key: %w", err)
	}
	return &k, nil
}

func (d *DB) ListAPIKeys() ([]types.APIKey, error) {
	rows, err := d.conn.Query(`
		SELECT key, name, plans, models, allowed_ips, rate_limit_rpm, rate_limit_rpd,
			monthly_token_limit, monthly_request_limit, expires_at, disabled, created_at, last_used_at, webhook_url, group_id
		FROM api_keys ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var results []types.APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		results = append(results, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

func (d *DB) UpdateAPIKey(key string, updates types.APIKey) error {
	plansJSON, err := marshalJSON(updates.Plans)
	if err != nil {
		return fmt.Errorf("update api key: plans: %w", err)
	}
	modelsJSON, err := marshalJSON(updates.Models)
	if err != nil {
		return fmt.Errorf("update api key: models: %w", err)
	}
	ipsJSON, err := marshalJSON(updates.AllowedIPs)
	if err != nil {
		return fmt.Errorf("update api key: allowed_ips: %w", err)
	}
	var expiresAt interface{}
	if updates.ExpiresAt != nil {
		expiresAt = *updates.ExpiresAt
	}
	var groupID interface{}
	if updates.GroupID != nil {
		groupID = *updates.GroupID
	}
	res, err := d.conn.Exec(`
		UPDATE api_keys SET
			name = ?, plans = ?, models = ?, allowed_ips = ?,
			rate_limit_rpm = ?, rate_limit_rpd = ?,
			monthly_token_limit = ?, monthly_request_limit = ?,
			expires_at = ?, disabled = ?, webhook_url = ?, group_id = ?
		WHERE key = ?
	`, updates.Name, string(plansJSON), string(modelsJSON), string(ipsJSON),
		updates.RateLimitRPM, updates.RateLimitRPD,
		updates.MonthlyTokenLimit, updates.MonthlyRequestLimit,
		expiresAt, updates.Disabled, updates.WebhookURL, groupID, key)
	if err != nil {
		return fmt.Errorf("update api key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("api key not found")
	}
	return nil
}

func (d *DB) DeleteAPIKey(key string) error {
	res, err := d.conn.Exec(`DELETE FROM api_keys WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("api key not found")
	}
	return nil
}

// --- Key Groups ---

func (d *DB) CreateKeyGroup(g types.KeyGroup) (int64, error) {
	res, err := d.conn.Exec(`
		INSERT INTO key_groups (name, monthly_token_limit, monthly_request_limit, monthly_budget_limit, webhook_url)
		VALUES (?, ?, ?, ?, ?)
	`, g.Name, g.MonthlyTokenLimit, g.MonthlyRequestLimit, g.MonthlyBudgetLimit, g.WebhookURL)
	if err != nil {
		return 0, fmt.Errorf("create key group: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (d *DB) GetKeyGroup(id int64) (*types.KeyGroup, error) {
	var g types.KeyGroup
	var webhookURL sql.NullString
	err := d.conn.QueryRow(`
		SELECT id, name, monthly_token_limit, monthly_request_limit, monthly_budget_limit, webhook_url
		FROM key_groups WHERE id = ?
	`, id).Scan(&g.ID, &g.Name, &g.MonthlyTokenLimit, &g.MonthlyRequestLimit, &g.MonthlyBudgetLimit, &webhookURL)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("key group not found")
	}
	if err != nil {
		return nil, fmt.Errorf("get key group: %w", err)
	}
	g.WebhookURL = webhookURL.String
	return &g, nil
}

func (d *DB) ListKeyGroups() ([]types.KeyGroup, error) {
	rows, err := d.conn.Query(`
		SELECT id, name, monthly_token_limit, monthly_request_limit, monthly_budget_limit, webhook_url
		FROM key_groups ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("list key groups: %w", err)
	}
	defer rows.Close()

	var results []types.KeyGroup
	for rows.Next() {
		var g types.KeyGroup
		var webhookURL sql.NullString
		if err := rows.Scan(&g.ID, &g.Name, &g.MonthlyTokenLimit, &g.MonthlyRequestLimit, &g.MonthlyBudgetLimit, &webhookURL); err != nil {
			return nil, fmt.Errorf("scan key group: %w", err)
		}
		g.WebhookURL = webhookURL.String
		results = append(results, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

func (d *DB) UpdateKeyGroup(id int64, g types.KeyGroup) error {
	res, err := d.conn.Exec(`
		UPDATE key_groups SET
			name = ?, monthly_token_limit = ?, monthly_request_limit = ?, monthly_budget_limit = ?, webhook_url = ?
		WHERE id = ?
	`, g.Name, g.MonthlyTokenLimit, g.MonthlyRequestLimit, g.MonthlyBudgetLimit, g.WebhookURL, id)
	if err != nil {
		return fmt.Errorf("update key group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("key group not found")
	}
	return nil
}

func (d *DB) DeleteKeyGroup(id int64) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(`UPDATE api_keys SET group_id = NULL WHERE group_id = ?`, id)
	if err != nil {
		return fmt.Errorf("clear key group refs: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM key_groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete key group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("key group not found")
	}
	return tx.Commit()
}

func (d *DB) GetGroupMonthlyUsage(groupID int64, year, month int) (*MonthlyUsage, error) {
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	var reqTokens, respTokens, reqCount sql.NullInt64
	err := d.conn.QueryRow(`
		SELECT COALESCE(SUM(request_tokens), 0), COALESCE(SUM(response_tokens), 0), COALESCE(COUNT(*), 0)
		FROM request_stats
		WHERE client_key IN (SELECT '****' || substr(key, -4) FROM api_keys WHERE group_id = ?)
			AND created_at >= ? AND created_at < ? AND status = 'success'
	`, groupID, start.UnixMilli(), end.UnixMilli()).Scan(&reqTokens, &respTokens, &reqCount)
	if err != nil {
		return nil, fmt.Errorf("query group monthly usage: %w", err)
	}
	return &MonthlyUsage{
		RequestTokens:  reqTokens.Int64,
		ResponseTokens: respTokens.Int64,
		RequestCount:   reqCount.Int64,
	}, nil
}

func (d *DB) UpdateKeyLastUsed(key string) error {
	_, err := d.conn.Exec(`UPDATE api_keys SET last_used_at = ? WHERE key = ?`, time.Now().Unix(), key)
	return err
}

func (d *DB) UpdateKeyLastUsedWithTime(key string, ts int64) error {
	_, err := d.conn.Exec(`UPDATE api_keys SET last_used_at = ? WHERE key = ?`, ts, key)
	return err
}

func (d *DB) GetKeyUsageSince(key string, since time.Time) (*WeeklyUsage, error) {
	var reqTokens, respTokens, reqCount sql.NullInt64
	err := d.conn.QueryRow(`
		SELECT COALESCE(SUM(request_tokens), 0), COALESCE(SUM(response_tokens), 0), COALESCE(COUNT(*), 0)
		FROM request_stats
		WHERE client_key = ? AND created_at > ? AND status = 'success'
	`, types.MaskAPIKey(key), since.UnixMilli()).Scan(&reqTokens, &respTokens, &reqCount)
	if err != nil {
		return nil, fmt.Errorf("query key usage: %w", err)
	}
	return &WeeklyUsage{
		RequestTokens:  reqTokens.Int64,
		ResponseTokens: respTokens.Int64,
		RequestCount:   reqCount.Int64,
	}, nil
}

func (d *DB) GetKeyMonthlyUsage(key string, year, month int) (*MonthlyUsage, error) {
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	var reqTokens, respTokens, reqCount sql.NullInt64
	err := d.conn.QueryRow(`
		SELECT COALESCE(SUM(request_tokens), 0), COALESCE(SUM(response_tokens), 0), COALESCE(COUNT(*), 0)
		FROM request_stats
		WHERE client_key = ? AND created_at >= ? AND created_at < ? AND status = 'success'
	`, types.MaskAPIKey(key), start.UnixMilli(), end.UnixMilli()).Scan(&reqTokens, &respTokens, &reqCount)
	if err != nil {
		return nil, fmt.Errorf("query monthly usage: %w", err)
	}
	return &MonthlyUsage{
		RequestTokens:  reqTokens.Int64,
		ResponseTokens: respTokens.Int64,
		RequestCount:   reqCount.Int64,
	}, nil
}

func (d *DB) RecordAudit(action, targetKey, actor, details string) error {
	_, err := d.conn.Exec(`
		INSERT INTO audit_log (action, target_key, actor, details)
		VALUES (?, ?, ?, ?)
	`, action, targetKey, actor, details)
	if err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}

func (d *DB) ListAuditLogs(limit int) ([]map[string]interface{}, error) {
	query := `SELECT id, action, target_key, actor, details, created_at FROM audit_log ORDER BY created_at DESC, id DESC`
	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = d.conn.Query(query+" LIMIT ?", limit)
	} else {
		rows, err = d.conn.Query(query)
	}
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var id int
		var action, targetKey, actor, details string
		var createdAt int64
		if err := rows.Scan(&id, &action, &targetKey, &actor, &details, &createdAt); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		results = append(results, map[string]interface{}{
			"id":         id,
			"action":     action,
			"target_key": targetKey,
			"actor":      actor,
			"details":    details,
			"created_at": createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

func (d *DB) SetModelPricing(model string, inputPrice, outputPrice float64) error {
	_, err := d.conn.Exec(`
		INSERT INTO model_pricing (model, input_price_per_1k, output_price_per_1k)
		VALUES (?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET
			input_price_per_1k = excluded.input_price_per_1k,
			output_price_per_1k = excluded.output_price_per_1k
	`, model, inputPrice, outputPrice)
	if err != nil {
		return fmt.Errorf("set pricing: %w", err)
	}
	return nil
}

func (d *DB) GetModelPricing(model string) (float64, float64, error) {
	var inputPrice, outputPrice float64
	err := d.conn.QueryRow(`
		SELECT input_price_per_1k, output_price_per_1k FROM model_pricing WHERE model = ?
	`, model).Scan(&inputPrice, &outputPrice)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("get pricing: %w", err)
	}
	return inputPrice, outputPrice, nil
}

func (d *DB) loadModelPricingMap() (map[string][2]float64, error) {
	rows, err := d.conn.Query(`SELECT model, input_price_per_1k, output_price_per_1k FROM model_pricing`)
	if err != nil {
		return nil, fmt.Errorf("load pricing map: %w", err)
	}
	defer rows.Close()

	pricing := make(map[string][2]float64)
	for rows.Next() {
		var model string
		var inPrice, outPrice float64
		if err := rows.Scan(&model, &inPrice, &outPrice); err != nil {
			return nil, fmt.Errorf("scan pricing: %w", err)
		}
		pricing[model] = [2]float64{inPrice, outPrice}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return pricing, nil
}

func (d *DB) getCachedPricingMap() (map[string][2]float64, error) {
	d.pricingCacheMu.RLock()
	if d.pricingCache != nil && time.Now().Before(d.pricingCacheUntil) {
		cache := d.pricingCache
		d.pricingCacheMu.RUnlock()
		return cache, nil
	}
	d.pricingCacheMu.RUnlock()

	pricing, err := d.loadModelPricingMap()
	if err != nil {
		return nil, err
	}

	d.pricingCacheMu.Lock()
	d.pricingCache = pricing
	d.pricingCacheUntil = time.Now().Add(5 * time.Minute)
	d.pricingCacheMu.Unlock()
	return pricing, nil
}

func (d *DB) GetKeyMonthlyCost(key string, year, month int) (float64, error) {
	pricing, err := d.getCachedPricingMap()
	if err != nil {
		return 0, err
	}

	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	rows, err := d.conn.Query(`
		SELECT model, request_tokens, response_tokens
		FROM request_stats
		WHERE client_key = ? AND created_at >= ? AND created_at < ? AND status = 'success'
	`, types.MaskAPIKey(key), start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("query stats for cost: %w", err)
	}
	defer rows.Close()

	totalCost := 0.0
	for rows.Next() {
		var model string
		var reqTokens, respTokens int
		if err := rows.Scan(&model, &reqTokens, &respTokens); err != nil {
			return 0, fmt.Errorf("scan stat: %w", err)
		}
		prices := pricing[model]
		totalCost += float64(reqTokens) / 1000.0 * prices[0]
		totalCost += float64(respTokens) / 1000.0 * prices[1]
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("rows error: %w", err)
	}
	return totalCost, nil
}
