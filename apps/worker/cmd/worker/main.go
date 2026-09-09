// Command worker drains the PostgreSQL processing-job queue.
//
// Scaffold status: the lease poll loop runs on the configured interval and
// shuts down cleanly. Stage ports (parsing, extraction, embeddings,
// evaluation, settlement) land here next, mirroring worker/main.py.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skillsignal/go-core/config"
)

func pollOnce(settings config.Settings) {
	// Next port: claim one leased processing job, then run the
	// parse -> extract -> embed -> evaluate -> settle pipeline.
	log.Print("poll processing jobs not yet implemented, skipping")
}

func main() {
	settings, err := config.Load()
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	if os.Getenv("DATABASE_URL") == "" {
		log.Print("warning: DATABASE_URL is empty, queue polling cannot reach postgres")
	}
	log.Printf("SkillSignal Go worker starting (poll %.1fs, lease %ds, llm=%t)",
		settings.WorkerPollIntervalSeconds, settings.WorkerLeaseSeconds, settings.LLMEnabled())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	interval := time.Duration(settings.WorkerPollIntervalSeconds * float64(time.Second))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Print("worker shutting down")
			return
		case <-ticker.C:
			pollOnce(settings)
		}
	}
}
