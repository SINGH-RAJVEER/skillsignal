// Command api applies SkillSignal's embedded PostgreSQL migrations.
package main

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/skillsignal/api/internal/migrate"
	"github.com/skillsignal/go-core/config"
	"github.com/skillsignal/go-core/db"
)

func main() {
	databaseURL, err := config.LoadDatabaseURL()
	if err != nil {
		log.Fatalf("invalid database configuration: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, db.NormalizeURL(databaseURL))
	if err != nil {
		log.Fatalf("migrate: connect: %v", err)
	}
	defer conn.Close(ctx)
	if err := migrate.Up(ctx, conn); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Print("migrate: up to date")
}
