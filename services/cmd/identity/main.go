package main

import "github.com/skillsignal/services/internal/servicehost"

func main() {
	servicehost.Run(servicehost.Definition{
		Name:        "identity",
		Owns:        []string{"users", "accounts", "employer organizations", "members", "join policies"},
		RouteGroups: []string{"auth", "organizations", "demo sessions"},
	})
}
