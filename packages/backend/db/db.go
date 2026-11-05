// Package db opens the shared PostgreSQL pool. It normalizes the
// SQLAlchemy-style postgresql+asyncpg:// scheme from compose into the
// postgresql:// form pgx expects.
package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var allSchemas = []string{"identity", "hiring", "documents", "evaluation", "billing", "platform", "public"}

// NormalizeURL converts SQLAlchemy driver URLs to pgx URLs.
func NormalizeURL(url string) string {
	if strings.HasPrefix(url, "postgresql+asyncpg://") {
		return "postgresql://" + strings.TrimPrefix(url, "postgresql+asyncpg://")
	}
	if strings.HasPrefix(url, "postgres://") {
		return url
	}
	return url
}

// Connect opens a pool with a pre-ping like the Python engine.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return ConnectFor(ctx, databaseURL, allSchemas...)
}

// ConnectFor opens a pool whose unqualified queries can see only the named schemas.
func ConnectFor(ctx context.Context, databaseURL string, schemas ...string) (*pgxpool.Pool, error) {
	if len(schemas) == 0 {
		return nil, fmt.Errorf("db: at least one schema is required")
	}
	config, err := pgxpool.ParseConfig(NormalizeURL(databaseURL))
	if err != nil {
		return nil, err
	}
	searchPath := SchemaSearchPath(schemas...)
	// Neon's proxy drops search_path from startup parameters, so set it on the
	// session. This requires a direct connection; a transaction pooler would not
	// keep the setting on one server connection.
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO "+searchPath)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// SchemaSearchPath quotes schema identifiers for a search_path setting.
func SchemaSearchPath(schemas ...string) string {
	quoted := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		quoted = append(quoted, `"`+strings.ReplaceAll(schema, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, ",")
}
