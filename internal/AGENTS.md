# internal/AGENTS.md: enclave service and host proxy

Go packages behind `cmd/permaily-enclave` (trusted, runs in the enclave) and `cmd/permaily-host` (untrusted, runs on the parent EC2 instance). Architecture: [../docs/architecture.md](../docs/architecture.md). Trust boundaries: [../docs/threat-model.md](../docs/threat-model.md).

| Package | Role |
|---|---|
| `enclave` | vsock listener, egress HTTP client through the host's CONNECT proxy (the parent instance is vsock CID 3), and the log sink that sends log lines to the host |
| `tlsacme` | one multi-SAN certificate via ACME TLS-ALPN-01; the certificate key and the ACME account key are kept sealed under the root secret (`Sealed`) and reused across restarts |
| `nsm`, `nsm/fakensm` | real NSM attester; fake attester for dev mode and tests |
| `httpapi` | `/attestation`, `/status` (the key that locks the root secret, the TLS identity), `/version`, mailbox API, `/v1/admin/handoff` |
| `router` | dispatch by Host (app, api, mcp, inbox) and security headers |
| `webui` | embeds the SPA build (`dist/` is copied in at build time) |
| `hostproxy` | TCP to vsock forwarding, allowlisted CONNECT proxy and printing of the enclave's log lines (host side) |
| `buildinfo` | commit set via ldflags |
| `smtpd` | MX on port 25 (vsock), STARTTLS with the enclave certificate, refuses mail without TLS (F1) |
| `inbox` | mailbox: parse, Gmail confirmation codes, seal, store, serve (F1) |
| `msgauth` | DKIM verification with DNS-over-HTTPS (F1) |
| `keys` | the root secret (`Root`): created in an enclave, stored one record per image under that image's KMS key, accepted only from an enclave of the same image or of a public release, handed only to a newer public release (ADR 0013); data keys wrapped under it; KMS with attestation (Recipient, CMS); the old mail key's policy reader for `/status`; local keys for dev |
| `envelope` | AES-256-GCM with key commitment and context binding (F1) |
| `store` | DynamoDB and S3 store for mailboxes, messages and the enclave's own sealed records; in-memory store for dev and tests (F1) |

## Rules

- `cmd/permaily-enclave` with `-tags dev` runs locally over TCP with a self-signed certificate and `fakensm`; never import `fakensm` from non-dev enclave code.
- No CGO, no files read at runtime inside the enclave; CA roots come from `x509roots/fallback`.
- New outbound destinations must be added to the host allowlist (`cmd/permaily-host`) and documented in [../docs/threat-model.md](../docs/threat-model.md).
- The TLS key is created once and reused across restarts and upgrades; `/attestation` binds `sha256(SPKI)` as user data. It leaves the enclave only sealed. When the sealed record cannot be loaded the enclave waits: never fall back to a fresh key, which would break the CAA records and the certificate history that verifiers check.
- Log lines leave the enclave (ADR 0015). Before adding one, read it as if it were public; never put an address, a subject, content, a token or a key in it, and keep `TestMailEndToEnd`'s log check passing.
- Nothing outside the enclave sees plaintext mail: seal with `envelope` under the mailbox data key before calling `store`; never log addresses, subjects or content.
- Every KMS response is checked against the key that was asked (`keys.ErrKeyMismatch`), every key must be in the pinned account and region, and no key is used before `kmspolicy.Sealed` accepts it. Never use KMS without the Recipient parameter.
- The root secret never leaves the enclave in the clear, not even to KMS. Never create one when a record cannot be read or opened: an image without its record waits. Never accept a root record, or hand the secret over, on anything but the checks in `keys.Root`; who asks is not one of them.
- Which builds are releases (repository, workflow, ref) and Sigstore's trust root are compiled into the image (`deploy/enclave.json`, `pkg/provenance`). A change to either changes who can receive the root secret: it needs an ADR.

## Commands

```sh
go test ./...
go run -tags dev ./cmd/permaily-enclave -port 8443   # needs web/dist copied to internal/webui/dist for the SPA
go run -tags dev ./cmd/permaily-enclave -port 8443 -mailbox-id dev -mailbox-address u-dev@inbox.permaily.com -mailbox-token-sha256 <hex>   # SMTP on :2525
go test -tags dev ./cmd/permaily-enclave             # whole service end to end
```
