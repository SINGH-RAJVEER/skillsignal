package main

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/servicehost"
	"github.com/skillsignal/billing/internal/httpapi"
)

func main() {
	settings, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	servicehost.Run(servicehost.Definition{Name: "billing", Owns: []string{"points", "orders", "payments", "entitlements"}, RegisterRoutes: func(mux *http.ServeMux, pool *pgxpool.Pool) { httpapi.Register(mux, pool, settings) }})
}
