module github.com/skillsignal/worker

go 1.26.0

require (
	github.com/skillsignal/go-core v0.0.0
	golang.org/x/crypto v0.57.0
	golang.org/x/text v0.42.0
)

require golang.org/x/sys v0.48.0 // indirect

replace github.com/skillsignal/go-core => ../../libs/go-core
