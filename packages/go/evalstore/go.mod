module github.com/callscreen/callscreen-platform/packages/go/evalstore

go 1.25.0

require github.com/callscreen/callscreen-platform/packages/go/evaluation v0.0.0

require (
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/text v0.39.0 // indirect
)

replace github.com/callscreen/callscreen-platform/packages/go/evaluation => ../evaluation

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics
