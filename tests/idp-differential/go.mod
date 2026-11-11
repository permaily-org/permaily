module github.com/permaily-org/permaily/tests/idp-differential

go 1.26.0

require (
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/permaily-org/permaily v0.0.0-00010101000000-000000000000
)

require (
	github.com/go-jose/go-jose/v4 v4.1.9 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
)

replace github.com/permaily-org/permaily => ../..
