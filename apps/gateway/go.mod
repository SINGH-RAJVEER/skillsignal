module github.com/skillsignal/gateway

go 1.26.0

require (
	github.com/skillsignal/backend v0.0.0
	github.com/skillsignal/contracts v0.0.0
)

replace github.com/skillsignal/contracts => ../../packages/contracts

replace github.com/skillsignal/backend => ../../packages/backend
