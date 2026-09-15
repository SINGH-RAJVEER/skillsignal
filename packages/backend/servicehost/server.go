// Package servicehost runs one domain HTTP process with a narrow ownership manifest.
package servicehost

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/db"
	"github.com/skillsignal/backend/httpx"
	"github.com/skillsignal/contracts/routes"
)

type Definition struct {
	Name        string   `json:"name"`
	Owns        []string `json:"owns"`
	RouteGroups []string `json:"routeGroups"`
}

func Run(definition Definition) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	databaseURL, err := config.LoadDatabaseURL()
	if err != nil {
		log.Fatalf("invalid database configuration: %v", err)
	}
	pool, err := db.ConnectFor(ctx, databaseURL, definition.Name, "platform", "public")
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": definition.Name})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		checkCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(checkCtx); err != nil {
			httpx.WriteError(w, http.StatusServiceUnavailable, httpx.CodeServiceUnavailable, "Database is unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "service": definition.Name})
	})
	mux.HandleFunc("GET /internal/service-info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, definition)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		owner, ok := routes.ServiceFor(r.URL.Path)
		if !ok || owner != definition.Name {
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "Route not found")
			return
		}
		httpx.NotImplemented(w, definition.Name+" HTTP handlers are being ported to their owning module")
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           httpx.WithLogging(httpx.WithBodyLimit(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("SkillSignal %s listening on %s", definition.Name, server.Addr)
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
