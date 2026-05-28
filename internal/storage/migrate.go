package storage

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrate runs all *.up.sql files in migrationsDir that have not yet been applied.
// It creates a schema_migrations table to track applied versions.
func Migrate(ctx context.Context, pool *pgxpool.Pool, migrationsDir string) error {
	if err := ensureMigrationsTable(ctx, pool); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}

	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	sort.Strings(files) // lexicographic order = numeric order given 001_, 002_, ...

	applied, err := appliedMigrations(ctx, pool)
	if err != nil {
		return fmt.Errorf("fetch applied migrations: %w", err)
	}

	for _, f := range files {
		version := migrationVersion(f)
		if applied[version] {
			slog.Debug("migration already applied", "version", version)
			continue
		}

		sql, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}

		slog.Info("applying migration", "version", version)
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("execute migration %s: %w", version, err)
		}

		if _, err := pool.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", version, err)
		}

		slog.Info("migration applied", "version", version)
	}

	return nil
}

func ensureMigrationsTable(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     VARCHAR(255) PRIMARY KEY,
			applied_at  TIMESTAMP NOT NULL DEFAULT NOW()
		)
	`)
	return err
}

func appliedMigrations(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// migrationVersion extracts "001_create_trades" from "migrations/001_create_trades.up.sql".
func migrationVersion(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".up.sql")
}
