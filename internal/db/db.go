package db

import (
	"database/sql"
	"fmt"

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

func (d *DB) Close() error {
	return d.conn.Close()
}
