package main

import "github.com/skillsignal/go-core/servicehost"

func main() {
	servicehost.Run(servicehost.Definition{
		Name:        "billing",
		Owns:        []string{"point accounts", "point ledger", "reservations", "weekly allowance", "Razorpay records", "enterprise entitlements"},
		RouteGroups: []string{"billing", "points", "payments", "entitlements"},
	})
}
