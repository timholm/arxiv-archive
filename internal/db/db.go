// Package db manages PostgreSQL connections, schema migrations, and provides
// the data access layer for the arxiv-archive.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a pgx connection pool with domain-specific query methods.
type DB struct {
	Pool *pgxpool.Pool
}

// New creates a new DB instance and establishes a connection pool.
func New(ctx context.Context, connStr string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("parse connection string: %w", err)
	}

	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &DB{Pool: pool}, nil
}

// Close releases all database connections.
func (db *DB) Close() {
	db.Pool.Close()
}

// Migrate runs the schema migration, creating tables and indexes if they don't exist.
func (db *DB) Migrate(ctx context.Context) error {
	migrations := []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,

		`CREATE TABLE IF NOT EXISTS papers (
			arxiv_id       TEXT PRIMARY KEY,
			title          TEXT NOT NULL,
			abstract       TEXT,
			authors        TEXT,
			categories     TEXT,
			published      TIMESTAMP,
			updated        TIMESTAMP,
			doi            TEXT,
			journal_ref    TEXT,
			has_full_text  BOOLEAN DEFAULT FALSE,
			full_text_path TEXT,
			embedding      vector(1536),
			fetched_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE INDEX IF NOT EXISTS idx_papers_categories ON papers(categories)`,
		`CREATE INDEX IF NOT EXISTS idx_papers_published ON papers(published)`,
		`CREATE INDEX IF NOT EXISTS idx_papers_fulltext ON papers(has_full_text)`,

		`CREATE TABLE IF NOT EXISTS refs (
			from_id TEXT NOT NULL,
			to_id   TEXT NOT NULL,
			PRIMARY KEY (from_id, to_id)
		)`,

		`CREATE INDEX IF NOT EXISTS idx_refs_to ON refs(to_id)`,

		`CREATE TABLE IF NOT EXISTS sync_state (
			key   TEXT PRIMARY KEY,
			value TEXT
		)`,
	}

	for _, m := range migrations {
		if _, err := db.Pool.Exec(ctx, m); err != nil {
			return fmt.Errorf("migration failed (%s): %w", truncate(m, 80), err)
		}
	}

	// Create IVFFlat index only if there are enough rows and the index doesn't exist.
	// IVFFlat requires at least lists * 10 rows to train properly.
	if err := db.maybeCreateVectorIndex(ctx); err != nil {
		// Non-fatal: index will be created later when enough data exists.
		fmt.Printf("note: vector index not created yet (need more data): %v\n", err)
	}

	return nil
}

// maybeCreateVectorIndex creates the IVFFlat index on the embedding column
// if there are enough rows and the index doesn't already exist.
func (db *DB) maybeCreateVectorIndex(ctx context.Context) error {
	// Check if index already exists
	var exists bool
	err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE indexname = 'idx_papers_embedding')`,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check index existence: %w", err)
	}
	if exists {
		return nil
	}

	// Check row count
	var count int64
	err = db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM papers WHERE embedding IS NOT NULL`).Scan(&count)
	if err != nil {
		return fmt.Errorf("count embedded papers: %w", err)
	}

	// IVFFlat with lists=100 needs at least 1000 rows
	if count < 1000 {
		return fmt.Errorf("only %d embedded papers, need 1000+ for IVFFlat index", count)
	}

	lists := 100
	if count < 10000 {
		lists = int(count / 10)
		if lists < 1 {
			lists = 1
		}
	}

	_, err = db.Pool.Exec(ctx, fmt.Sprintf(
		`CREATE INDEX idx_papers_embedding ON papers USING ivfflat (embedding vector_cosine_ops) WITH (lists = %d)`,
		lists,
	))
	if err != nil {
		return fmt.Errorf("create vector index: %w", err)
	}

	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Stats holds archive statistics.
type Stats struct {
	TotalPapers      int64     `json:"total_papers"`
	WithFullText     int64     `json:"with_full_text"`
	WithEmbedding    int64     `json:"with_embedding"`
	TotalRefs        int64     `json:"total_refs"`
	CategoriesCount  int64     `json:"categories_count"`
	OldestPaper      time.Time `json:"oldest_paper"`
	NewestPaper      time.Time `json:"newest_paper"`
	LastSync         string    `json:"last_sync"`
	PapersLast7Days  int64     `json:"papers_last_7_days"`
	PapersLast30Days int64     `json:"papers_last_30_days"`
}

// GetStats returns archive statistics.
func (db *DB) GetStats(ctx context.Context) (*Stats, error) {
	s := &Stats{}

	queries := []struct {
		query string
		dest  interface{}
	}{
		{`SELECT COUNT(*) FROM papers`, &s.TotalPapers},
		{`SELECT COUNT(*) FROM papers WHERE has_full_text = TRUE`, &s.WithFullText},
		{`SELECT COUNT(*) FROM papers WHERE embedding IS NOT NULL`, &s.WithEmbedding},
		{`SELECT COUNT(*) FROM refs`, &s.TotalRefs},
		{`SELECT COUNT(*) FROM papers WHERE published >= NOW() - INTERVAL '7 days'`, &s.PapersLast7Days},
		{`SELECT COUNT(*) FROM papers WHERE published >= NOW() - INTERVAL '30 days'`, &s.PapersLast30Days},
	}

	for _, q := range queries {
		if err := db.Pool.QueryRow(ctx, q.query).Scan(q.dest); err != nil {
			return nil, fmt.Errorf("stats query failed (%s): %w", truncate(q.query, 60), err)
		}
	}

	// Oldest/newest paper dates (may not exist if db is empty)
	_ = db.Pool.QueryRow(ctx, `SELECT COALESCE(MIN(published), '1970-01-01') FROM papers`).Scan(&s.OldestPaper)
	_ = db.Pool.QueryRow(ctx, `SELECT COALESCE(MAX(published), '1970-01-01') FROM papers`).Scan(&s.NewestPaper)

	// Distinct categories (stored as comma-separated in a text column)
	_ = db.Pool.QueryRow(ctx,
		`SELECT COUNT(DISTINCT unnest) FROM (SELECT unnest(string_to_array(categories, ',')) FROM papers) sub`,
	).Scan(&s.CategoriesCount)

	// Last sync time
	var lastSync *string
	_ = db.Pool.QueryRow(ctx, `SELECT value FROM sync_state WHERE key = 'last_sync'`).Scan(&lastSync)
	if lastSync != nil {
		s.LastSync = *lastSync
	}

	return s, nil
}

// SetSyncState upserts a key-value pair in the sync_state table.
func (db *DB) SetSyncState(ctx context.Context, key, value string) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO sync_state (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		key, value,
	)
	return err
}

// GetSyncState retrieves a value from the sync_state table.
func (db *DB) GetSyncState(ctx context.Context, key string) (string, error) {
	var value string
	err := db.Pool.QueryRow(ctx,
		`SELECT value FROM sync_state WHERE key = $1`, key,
	).Scan(&value)
	if err != nil {
		return "", err
	}
	return value, nil
}
