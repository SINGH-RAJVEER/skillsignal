// Command worker drains the PostgreSQL processing-job queue.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/db"
	"github.com/skillsignal/backend/storage"
	"github.com/skillsignal/contracts/jobs"
	"github.com/skillsignal/worker/internal/pipeline"
)

func main() {
	role, err := jobs.ParseRole(os.Getenv("WORKER_ROLE"))
	if err != nil {
		log.Fatalf("invalid worker role: %v", err)
	}
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
	if role == jobs.ParserRole && settings.LLMEnabled() {
		log.Fatal("parser worker must not receive OPENROUTER_API_KEY")
	}
	log.Printf("SkillSignal %s worker starting (poll %.1fs, lease %ds, llm=%t)",
		role, settings.WorkerPollIntervalSeconds, settings.WorkerLeaseSeconds, settings.LLMEnabled())
	pipeline.New(pool, settings, store, role).Run(ctx)
	log.Print("worker shutting down")
}
