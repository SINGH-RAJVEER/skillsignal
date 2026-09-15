package main

import "github.com/skillsignal/services/internal/servicehost"

func main() {
	servicehost.Run(servicehost.Definition{
		Name:        "hiring",
		Owns:        []string{"jobs", "job requirements", "candidate records", "invitations", "resume submissions", "review decisions"},
		RouteGroups: []string{"jobs", "invitations", "submissions", "exports"},
	})
}
