// Command api serves the SkillSignal HTTP API.
//
// Scaffold status: health and middleware are live. Domain routes under /api/
// return 503 with the contract error envelope until each group is ported
// from the Python implementation (auth, jobs, resumes, billing, demo, admin).
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

	"github.com/skillsignal/go-core/config"
	"github.com/skillsignal/go-core/httpx"
)

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "api"})
}

func notImplementedHandler(w http.ResponseWriter, _ *http.Request) {
	httpx.NotImplemented(w, "")
}

func retentionSweepLoop(ctx context.Context, intervalSeconds int) {
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
			log.Print("retention sweep not yet implemented, skipping")
		}
	}
}

func main() {
	settings, err := config.Load()
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go retentionSweepLoop(ctx, settings.RetentionSweepIntervalSeconds)

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
