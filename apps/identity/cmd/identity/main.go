package main

import (
	"context"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/retention"
	"github.com/skillsignal/backend/servicehost"
	"github.com/skillsignal/identity/internal/httpapi"
)

func main() {
	settings, err := config.Load()
	if err != nil {
		log.Fatalf("invalid identity configuration: %v", err)
	}
	servicehost.Run(servicehost.Definition{
		Name:        "identity",
		Owns:        []string{"users", "accounts", "employer organizations", "members", "join policies"},
		RouteGroups: []string{"auth", "organizations", "demo sessions"},
		Start: func(ctx context.Context, _ *pgxpool.Pool) {
			retention.Start(ctx, settings)
		},
		RegisterRoutes: func(mux *http.ServeMux, pool *pgxpool.Pool) {
			httpapi.Register(mux, httpapi.NewPostgresStore(pool), settings.JWTSecret, settings.JWTTTL)
		},
	})
}
