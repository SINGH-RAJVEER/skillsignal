package main

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/servicehost"
	"github.com/skillsignal/documents/internal/httpapi"
)

func main() {
	settings, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	servicehost.Run(servicehost.Definition{Name: "documents", Owns: []string{"resume documents", "resume versions", "object storage"}, RegisterRoutes: func(mux *http.ServeMux, pool *pgxpool.Pool) { httpapi.Register(mux, pool, settings) }})
}
