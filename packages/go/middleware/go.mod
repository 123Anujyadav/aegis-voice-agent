module github.com/callscreen/callscreen-platform/packages/go/middleware

go 1.23.0

require (
	github.com/callscreen/callscreen-platform/packages/go/platform v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/redis v0.0.0
)

replace github.com/callscreen/callscreen-platform/packages/go/platform => ../platform
replace github.com/callscreen/callscreen-platform/packages/go/redis => ../redis
