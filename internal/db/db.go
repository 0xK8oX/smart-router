package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	`CREATE TABLE IF NOT EXISTS plans (
		slug TEXT PRIMARY KEY,
		config TEXT NOT NULL
	);`,
}

type DB struct {
	conn *sql.DB
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

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

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
	streaming := 0
	if r.IsStreaming {
		streaming = 1
	}

	_, err := d.conn.Exec(`
		INSERT INTO request_stats
			(plan, provider, model, key_mask, request_tokens, response_tokens, total_tokens, status, latency_ms, is_streaming, target_provider)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.Plan, r.Provider, r.Model, r.KeyMask, r.RequestTokens, r.ResponseTokens, r.TotalTokens, r.Status, r.LatencyMs, streaming, r.TargetProvider)

	if err != nil {
		return fmt.Errorf("insert stat: %w", err)
	}
	return nil
}

func (d *DB) GetStats(plan, provider string, limit int) ([]types.StatRecord, error) {
	query := `
		SELECT plan, provider, model, key_mask, request_tokens, response_tokens, total_tokens, status, latency_ms, is_streaming, target_provider
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
	return d.conn.Close()
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
