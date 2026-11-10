# internal/AGENTS.md: enclave service and host proxy

Go packages behind `cmd/permaily-enclave` (trusted, runs in the enclave) and `cmd/permaily-host` (untrusted, runs on the parent EC2 instance). Architecture: [../docs/architecture.md](../docs/architecture.md). Trust boundaries: [../docs/threat-model.md](../docs/threat-model.md).

| Package | Role |
|---|---|
| `enclave` | vsock listener and egress HTTP client through the host's CONNECT proxy (the parent instance is vsock CID 3) |
| `tlsacme` | one multi-SAN certificate via ACME TLS-ALPN-01; key lives only in enclave memory |
| `nsm`, `nsm/fakensm` | real NSM attester; fake attester for dev mode and tests |
| `httpapi` | `/attestation`, `/status` (KMS key policy), `/version`, mailbox API |
| `router` | dispatch by Host (app, api, mcp, inbox) and security headers |
| `webui` | embeds the SPA build (`dist/` is copied in at build time) |
| `hostproxy` | TCP to vsock forwarding and allowlisted CONNECT proxy (host side) |
| `buildinfo` | commit set via ldflags |
| `smtpd` | MX on port 25 (vsock), STARTTLS with the enclave certificate (F1) |
| `inbox` | mailbox: parse, Gmail confirmation codes, seal, store, serve (F1) |
| `msgauth` | DKIM verification with DNS-over-HTTPS (F1) |
| `keys` | data keys from KMS with attestation (Recipient, CMS), reader of the key's policy and grants for `/status`, local keys for dev (F1) |
| `envelope` | AES-256-GCM with key commitment and context binding (F1) |
| `store` | DynamoDB and S3 store, in-memory store for dev and tests (F1) |

## Rules

- `cmd/permaily-enclave` with `-tags dev` runs locally over TCP with a self-signed certificate and `fakensm`; never import `fakensm` from non-dev enclave code.
- No CGO, no files read at runtime inside the enclave; CA roots come from `x509roots/fallback`.
- New outbound destinations must be added to the host allowlist (`cmd/permaily-host`) and documented in [../docs/threat-model.md](../docs/threat-model.md).
- The TLS key must stay constant for the enclave lifetime; `/attestation` binds `sha256(SPKI)` as user data.
- Nothing outside the enclave sees plaintext mail: seal with `envelope` under the mailbox data key before calling `store`; never log addresses, subjects or content.
- Every KMS response is checked against the pinned key ARN (`keys.ErrKeyMismatch`); never use KMS without the Recipient parameter.

## Commands

```sh
go test ./...
go run -tags dev ./cmd/permaily-enclave -port 8443   # needs web/dist copied to internal/webui/dist for the SPA
go run -tags dev ./cmd/permaily-enclave -port 8443 -mailbox-id dev -mailbox-address u-dev@inbox.permaily.com -mailbox-token-sha256 <hex>   # SMTP on :2525
go test -tags dev ./cmd/permaily-enclave             # whole service end to end
```
