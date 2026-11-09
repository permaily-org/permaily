# pkg/attestation/AGENTS.md: verifier library and CLI

Public Go package that verifies AWS Nitro attestation documents, used by `cmd/permaily-verify` and reusable by third parties. Protocol: [../../docs/verification.md](../../docs/verification.md).

## Rules

- This package is the security core of verification. Every check must have a negative test (`verify_test.go`, `remote_test.go`).
- The AWS root is embedded (`aws_nitro_enclaves_root_g1.pem`) and pinned by fingerprint; `TestAWSRootFingerprint` must keep passing.
- Keep dependencies minimal (stdlib and `fxamacker/cbor`).
- `attestationtest` builds documents with a throwaway chain for tests; it must never be accepted with the default (AWS) root.
- Changing `UserDataScheme` or the response format of `/attestation` is a breaking change for every verifier: version it.
