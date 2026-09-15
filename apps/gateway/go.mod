module github.com/skillsignal/gateway

go 1.26.0

require (
	github.com/skillsignal/contracts v0.0.0
	github.com/skillsignal/go-core v0.0.0
)

replace github.com/skillsignal/contracts => ../../libs/contracts

replace github.com/skillsignal/go-core => ../../libs/go-core
