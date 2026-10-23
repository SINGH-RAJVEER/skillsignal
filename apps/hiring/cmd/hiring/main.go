package main

import (
	"context"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/servicehost"
	"github.com/skillsignal/hiring/internal/httpapi"
)

func main() {
	settings, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	servicehost.Run(servicehost.Definition{
		Name:        "hiring",
		Owns:        []string{"jobs", "job requirements", "candidate records", "invitations", "resume submissions", "review decisions"},
		RouteGroups: []string{"jobs", "invitations", "submissions", "exports"},
		RegisterRoutes: func(mux *http.ServeMux, pool *pgxpool.Pool) {
			httpapi.Register(mux, pool, settings)
		},
		Start: func(ctx context.Context, pool *pgxpool.Pool) {
			httpapi.Start(ctx, pool, settings)
		},
	})
}
