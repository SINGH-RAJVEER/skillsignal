// Command worker drains the PostgreSQL processing-job queue.
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/skillsignal/go-core/config"
	"github.com/skillsignal/go-core/db"
	"github.com/skillsignal/go-core/storage"
	"github.com/skillsignal/worker/internal/pipeline"
)

func main() {
	settings, err := config.Load()
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
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
	log.Printf("SkillSignal Go worker starting (poll %.1fs, lease %ds, llm=%t)",
		settings.WorkerPollIntervalSeconds, settings.WorkerLeaseSeconds, settings.LLMEnabled())
	pipeline.New(pool, settings, store).Run(ctx)
	log.Print("worker shutting down")
}
