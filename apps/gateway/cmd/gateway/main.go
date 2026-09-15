package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skillsignal/contracts/routes"
	"github.com/skillsignal/go-core/httpx"
)

type upstream struct {
	name  string
	url   *url.URL
	proxy *httputil.ReverseProxy
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	upstreams := loadUpstreams()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "gateway"})
	})
	mux.HandleFunc("GET /readyz", readiness(upstreams))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		owner, ok := routes.ServiceFor(r.URL.Path)
		if !ok {
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "Route not found")
			return
		}
		upstreams[owner].proxy.ServeHTTP(w, r)
	})

	port := envOr("PORT", "8000")
	webURL := envOr("WEB_URL", "http://localhost:3000")
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           httpx.WithLogging(httpx.WithOriginGuard(webURL)(httpx.WithBodyLimit(mux))),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("SkillSignal gateway listening on %s", server.Addr)
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

func loadUpstreams() map[string]upstream {
	settings := map[string]string{
		routes.Identity:   envOr("IDENTITY_URL", "http://identity:8080"),
		routes.Hiring:     envOr("HIRING_URL", "http://hiring:8080"),
		routes.Documents:  envOr("DOCUMENTS_URL", "http://documents:8080"),
		routes.Evaluation: envOr("EVALUATION_URL", "http://evaluation:8080"),
		routes.Billing:    envOr("BILLING_URL", "http://billing:8080"),
	}
	result := make(map[string]upstream, len(settings))
	for name, raw := range settings {
		target, err := url.Parse(raw)
		if err != nil || target.Scheme == "" || target.Host == "" {
			log.Fatalf("invalid %s upstream URL %q", name, raw)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Printf("upstream request failed: %v", err)
			httpx.WriteError(w, http.StatusBadGateway, httpx.CodeServiceUnavailable, "Upstream service is unavailable")
		}
		result[name] = upstream{name: name, url: target, proxy: proxy}
	}
	return result
}

func readiness(upstreams map[string]upstream) http.HandlerFunc {
	client := &http.Client{Timeout: 2 * time.Second}
	return func(w http.ResponseWriter, r *http.Request) {
		for _, target := range upstreams {
			request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.url.String()+"/readyz", nil)
			if err != nil {
				httpx.WriteError(w, http.StatusServiceUnavailable, httpx.CodeServiceUnavailable, "Gateway configuration is invalid")
				return
			}
			response, err := client.Do(request)
			if err != nil || response.StatusCode != http.StatusOK {
				if response != nil {
					response.Body.Close()
				}
				httpx.WriteError(w, http.StatusServiceUnavailable, httpx.CodeServiceUnavailable, target.name+" service is unavailable")
				return
			}
			response.Body.Close()
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "service": "gateway"})
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
