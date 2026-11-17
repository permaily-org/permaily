module github.com/permaily-org/permaily/tests/oauth-differential

go 1.26.0

replace github.com/permaily-org/permaily => ../..

require (
	github.com/go-jose/go-jose/v4 v4.1.9
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/permaily-org/permaily v0.0.0-00010101000000-000000000000
	golang.org/x/oauth2 v0.36.0
)

require (
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
)
