// Usage: api serve (default) runs the server; api migrate applies pending SQL migrations and exits, mirroring migrate.py upgrade head.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skillsignal/go-core/config"
	"github.com/skillsignal/go-core/db"
	"github.com/skillsignal/go-core/httpx"
	"github.com/skillsignal/go-core/retention"
	"github.com/skillsignal/go-core/storage"

	"github.com/skillsignal/api/internal/migrate"
)

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "api"})
}

func notImplementedHandler(w http.ResponseWriter, _ *http.Request) {
	httpx.NotImplemented(w, "")
}

func retentionSweep(ctx context.Context, pool *pgxpool.Pool, store *storage.Store, intervalSeconds int) {
	if intervalSeconds <= 0 {
		return
	}
	ticker := time.NewTicker(time.Duration(intervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tx, err := pool.Begin(ctx)
			if err != nil {
				log.Printf("retention sweep: begin: %v", err)
				continue
			}
			result, err := retention.PurgeExpiredData(ctx, tx, store, time.Now().UTC())
			if err != nil {
				_ = tx.Rollback(ctx)
				log.Printf("retention sweep: %v", err)
				continue
			}
			if err := tx.Commit(ctx); err != nil {
				log.Printf("retention sweep: commit: %v", err)
				continue
			}
			log.Printf("retention sweep: removed %d documents and %d independent evaluations", result.DocumentsPurged, result.IndependentEvaluationsPurged)
		}
	}
}

func runMigrate(settings config.Settings) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, db.NormalizeURL(settings.DatabaseURL))
	if err != nil {
		log.Fatalf("migrate: connect: %v", err)
	}
	defer conn.Close(ctx)
	if err := migrate.Up(ctx, conn); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Print("migrate: up to date")
}

func main() {
	settings, err := config.Load()
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "migrate" {
		runMigrate(settings)
		return
	}
	if command != "serve" {
		log.Fatalf("unknown command %q (want serve or migrate)", command)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := db.Connect(ctx, settings.DatabaseURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()
	store, err := storage.New(settings.StorageRoot)
	if err != nil {
		log.Fatalf("configure storage: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.HandleFunc("/api/", notImplementedHandler)

	handler := httpx.WithLogging(httpx.WithOriginGuard(settings.WebURL)(httpx.WithBodyLimit(mux)))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go retentionSweep(ctx, pool, store, settings.RetentionSweepIntervalSeconds)

	go func() {
		log.Printf("SkillSignal Go API listening on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
