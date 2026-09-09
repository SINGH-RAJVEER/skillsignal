// Package migrate applies the embedded SQL migrations in filename order,
// tracking them in schema_migrations. It replaces Alembic for the Go port;
// the baseline captures the Python migration end-state.
package migrate

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed all:migrations
var files embed.FS

// Up applies every pending migration inside one transaction each.
func Up(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("migrate: prepare tracking table: %w", err)
	}
	entries, err := files.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("migrate: list migrations: %w", err)
	}
	var ups []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			ups = append(ups, entry.Name())
		}
	}
	sort.Strings(ups)
	for _, name := range ups {
		version := strings.TrimSuffix(name, ".up.sql")
		var exists bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
			return fmt.Errorf("migrate: check %s: %w", version, err)
		}
		if exists {
			continue
		}
		raw, err := files.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("migrate: begin %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, string(raw)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migrate: apply %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migrate: record %s: %w", version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migrate: commit %s: %w", version, err)
		}
	}
	return nil
}
