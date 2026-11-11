module github.com/permaily-org/permaily/tests/webauthn-differential

go 1.26.0

require (
	github.com/descope/virtualwebauthn v1.0.5
	github.com/fxamacker/cbor/v2 v2.10.0
	github.com/go-webauthn/webauthn v0.18.2
	github.com/permaily-org/permaily v0.0.0-00010101000000-000000000000
)

require (
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.3.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/permaily-org/permaily => ../..
