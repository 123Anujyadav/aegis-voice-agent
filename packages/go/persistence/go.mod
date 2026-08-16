module github.com/callscreen/callscreen-platform/packages/go/persistence

go 1.23.0

require (
	github.com/callscreen/callscreen-platform/packages/go/eventbus v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/outbox v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/platform v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/repository v0.0.0
)

replace github.com/callscreen/callscreen-platform/packages/go/eventbus => ../eventbus
replace github.com/callscreen/callscreen-platform/packages/go/outbox => ../outbox
replace github.com/callscreen/callscreen-platform/packages/go/platform => ../platform
replace github.com/callscreen/callscreen-platform/packages/go/repository => ../repository
