// Package db opens the shared PostgreSQL pool. It normalizes the
// SQLAlchemy-style postgresql+asyncpg:// scheme from compose into the
// postgresql:// form pgx expects.
package db

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
	pool, err := pgxpool.New(ctx, NormalizeURL(databaseURL))
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
