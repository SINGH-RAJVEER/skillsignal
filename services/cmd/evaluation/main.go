package main

import "github.com/skillsignal/services/internal/servicehost"

func main() {
	servicehost.Run(servicehost.Definition{
		Name:        "evaluation",
		Owns:        []string{"independent evaluations", "batch evaluations", "requirement assessments", "embeddings", "scores", "ranking"},
		RouteGroups: []string{"evaluations", "independent evaluations", "processing status"},
	})
}
