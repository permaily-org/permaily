# internal/AGENTS.md: enclave service and host proxy

Go packages behind `cmd/permaily-enclave` (trusted, runs in the enclave) and `cmd/permaily-host` (untrusted, runs on the parent EC2 instance). Design: [../docs/design.md](../docs/design.md). Trust boundaries: [../docs/security.md](../docs/security.md).

| Package | Role |
|---|---|
| `enclave` | vsock listener and egress HTTP client through the host's CONNECT proxy |
| `tlsacme` | one multi-SAN certificate via ACME TLS-ALPN-01; key lives only in enclave memory |
| `nsm`, `nsm/fakensm` | real NSM attester; fake attester for dev mode and tests |
| `httpapi` | `/attestation`, `/version` |
| `router` | dispatch by Host (app, api, mcp, inbox) and security headers |
| `webui` | embeds the SPA build (`dist/` is copied in at build time) |
| `hostproxy` | TCP to vsock forwarding and allowlisted CONNECT proxy (host side) |
| `buildinfo` | commit set via ldflags |

## Rules

- `cmd/permaily-enclave` with `-tags dev` runs locally over TCP with a self-signed certificate and `fakensm`; never import `fakensm` from non-dev enclave code.
- No CGO, no files read at runtime inside the enclave; CA roots come from `x509roots/fallback`.
- New outbound destinations must be added to the host allowlist (`cmd/permaily-host`) and documented in [../docs/security.md](../docs/security.md).
- The TLS key must stay constant for the enclave lifetime; `/attestation` binds `sha256(SPKI)` as user data.

## Commands

```sh
go test ./...
go run -tags dev ./cmd/permaily-enclave -port 8443   # needs web/dist copied to internal/webui/dist for the SPA
```
