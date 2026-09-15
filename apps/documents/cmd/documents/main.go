package main

import "github.com/skillsignal/go-core/servicehost"

func main() {
	servicehost.Run(servicehost.Definition{
		Name:        "documents",
		Owns:        []string{"resume documents", "resume versions", "storage metadata", "retention"},
		RouteGroups: []string{"documents", "resumes", "uploads"},
	})
}
