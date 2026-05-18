package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
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
	`CREATE INDEX IF NOT EXISTS idx_stats_client_key ON request_stats(client_key);`,
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
	conn     *sql.DB
	statChan chan types.StatRecord
	stopChan chan struct{}
	wg       sync.WaitGroup
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
	return db, nil
}

func (d *DB) migrate() error {
	for _, stmt := range migrations {
		if _, err := d.conn.Exec(stmt); err != nil {
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
			(plan, provider, model, key_mask, client_key, request_tokens, response_tokens, total_tokens, status, latency_ms, is_streaming, target_provider)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			r.Plan, r.Provider, r.Model, r.KeyMask, r.ClientKey,
			r.RequestTokens, r.ResponseTokens, r.TotalTokens,
			r.Status, r.LatencyMs, streaming, r.TargetProvider,
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

// RecordStatAsync sends a stat record to the background worker.
// If the channel is full, it falls back to synchronous insertion.
func (d *DB) RecordStatAsync(r types.StatRecord) {
	select {
	case d.statChan <- r:
	default:
		// Channel full — write synchronously so we don't drop stats
		if err := d.RecordStat(r); err != nil {
			log.Printf("[DB] sync stat insert failed: %v", err)
		}
	}
}

func (d *DB) GetStats(plan, provider string, limit int) ([]types.StatRecord, error) {
	query := `
		SELECT plan, provider, model, key_mask, client_key, request_tokens, response_tokens, total_tokens, status, latency_ms, is_streaming, target_provider
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

	query += ` ORDER BY created_at DESC`

	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query stats: %w", err)
	}
	defer rows.Close()

	var results []types.StatRecord
	for rows.Next() {
		var r types.StatRecord
		var streaming int
		err := rows.Scan(
			&r.Plan,
			&r.Provider,
			&r.Model,
			&r.KeyMask,
			&r.ClientKey,
			&r.RequestTokens,
			&r.ResponseTokens,
			&r.TotalTokens,
			&r.Status,
			&r.LatencyMs,
			&streaming,
			&r.TargetProvider,
		)
		if err != nil {
			return nil, fmt.Errorf("scan stat: %w", err)
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
		plans[slug] = config
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return plans, nil
}

func (d *DB) DeletePlan(slug string) error {
	_, err := d.conn.Exec(`DELETE FROM plans WHERE slug = ?`, slug)
	if err != nil {
		return fmt.Errorf("delete plan: %w", err)
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
	_ = json.Unmarshal([]byte(plansJSON), &k.Plans)
	_ = json.Unmarshal([]byte(modelsJSON), &k.Models)
	_ = json.Unmarshal([]byte(ipsJSON), &k.AllowedIPs)
	return k, nil
}

func (d *DB) CreateAPIKey(key types.APIKey) error {
	plansJSON, _ := json.Marshal(key.Plans)
	modelsJSON, _ := json.Marshal(key.Models)
	ipsJSON, _ := json.Marshal(key.AllowedIPs)
	var expiresAt interface{}
	if key.ExpiresAt != nil {
		expiresAt = *key.ExpiresAt
	}
	_, err := d.conn.Exec(`
		INSERT INTO api_keys
			(key, name, plans, models, allowed_ips, rate_limit_rpm, rate_limit_rpd, monthly_token_limit, monthly_request_limit, expires_at, disabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, key.Key, key.Name, string(plansJSON), string(modelsJSON), string(ipsJSON),
		key.RateLimitRPM, key.RateLimitRPD, key.MonthlyTokenLimit, key.MonthlyRequestLimit,
		expiresAt, 0, key.CreatedAt)
	if err != nil {
		return fmt.Errorf("create api key: %w", err)
	}
	return nil
}

func (d *DB) GetAPIKey(key string) (*types.APIKey, error) {
	rows, err := d.conn.Query(`
		SELECT key, name, plans, models, allowed_ips, rate_limit_rpm, rate_limit_rpd,
			monthly_token_limit, monthly_request_limit, expires_at, disabled, created_at, last_used_at
		FROM api_keys WHERE key = ?
	`, key)
	if err != nil {
		return nil, fmt.Errorf("query api key: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
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
			monthly_token_limit, monthly_request_limit, expires_at, disabled, created_at, last_used_at
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
	plansJSON, _ := json.Marshal(updates.Plans)
	modelsJSON, _ := json.Marshal(updates.Models)
	ipsJSON, _ := json.Marshal(updates.AllowedIPs)
	var expiresAt interface{}
	if updates.ExpiresAt != nil {
		expiresAt = *updates.ExpiresAt
	}
	_, err := d.conn.Exec(`
		UPDATE api_keys SET
			name = ?, plans = ?, models = ?, allowed_ips = ?,
			rate_limit_rpm = ?, rate_limit_rpd = ?,
			monthly_token_limit = ?, monthly_request_limit = ?,
			expires_at = ?, disabled = ?
		WHERE key = ?
	`, updates.Name, string(plansJSON), string(modelsJSON), string(ipsJSON),
		updates.RateLimitRPM, updates.RateLimitRPD,
		updates.MonthlyTokenLimit, updates.MonthlyRequestLimit,
		expiresAt, updates.Disabled, key)
	if err != nil {
		return fmt.Errorf("update api key: %w", err)
	}
	return nil
}

func (d *DB) DeleteAPIKey(key string) error {
	_, err := d.conn.Exec(`DELETE FROM api_keys WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	return nil
}

func (d *DB) UpdateKeyLastUsed(key string) error {
	_, err := d.conn.Exec(`UPDATE api_keys SET last_used_at = ? WHERE key = ?`, time.Now().Unix(), key)
	return err
}

func (d *DB) GetKeyUsageSince(key string, since time.Time) (*WeeklyUsage, error) {
	var reqTokens, respTokens, reqCount sql.NullInt64
	err := d.conn.QueryRow(`
		SELECT COALESCE(SUM(request_tokens), 0), COALESCE(SUM(response_tokens), 0), COALESCE(COUNT(*), 0)
		FROM request_stats
		WHERE client_key = ? AND created_at > ? AND status = 'success'
	`, key, since.UnixMilli()).Scan(&reqTokens, &respTokens, &reqCount)
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
	`, key, start.UnixMilli(), end.UnixMilli()).Scan(&reqTokens, &respTokens, &reqCount)
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
	query := `SELECT id, action, target_key, actor, details, created_at FROM audit_log ORDER BY created_at DESC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := d.conn.Query(query)
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

func (d *DB) GetKeyMonthlyCost(key string, year, month int) (float64, error) {
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	rows, err := d.conn.Query(`
		SELECT model, request_tokens, response_tokens
		FROM request_stats
		WHERE client_key = ? AND created_at >= ? AND created_at < ? AND status = 'success'
	`, key, start.UnixMilli(), end.UnixMilli())
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
		inPrice, outPrice, _ := d.GetModelPricing(model)
		totalCost += float64(reqTokens) / 1000.0 * inPrice
		totalCost += float64(respTokens) / 1000.0 * outPrice
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("rows error: %w", err)
	}
	return totalCost, nil
}
