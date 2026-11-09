# Architecture

## Goal

Apps such as travel assistants ask for full Gmail access to read a few messages. Permaily sits in between: the user connects their mailbox once, and each app or AI agent receives only the subset of messages the user authorized. Rules are enforced on every delivery, with an audit log and revocation.

What sets Permaily apart is verifiability: the whole service runs inside an AWS Nitro Enclave built reproducibly from this repository, so anyone can check that the code running is the published code. See [verification.md](verification.md) and [threat-model.md](threat-model.md). Phases and planned work are in [plan.md](plan.md); the visual design is in [design.md](design.md).

## Components (F0)

```
client / verifier ──TLS──▶ EIP:443 ─▶ permaily-host (parent EC2, untrusted) ─vsock 443─▶ permaily-enclave
                                                                                         ├─ ACME TLS-ALPN-01, key only in RAM
                         permaily-host vsock 8000 (CONNECT allowlist) ◀──────────────────┤  egress (Let's Encrypt)
                                                                                         └─ /dev/nsm → attestation(nonce, sha256(SPKI))
```

- **permaily-enclave** (`cmd/permaily-enclave`): one Go binary. Terminates TLS, obtains one certificate for all hosts, dispatches by Host:

  | Host | Serves |
  |---|---|
  | `app.permaily.com` | web app (see [web.md](web.md)), embedded with `go:embed` |
  | `api.permaily.com` | API for integrated apps (F2) |
  | `mcp.permaily.com` | remote MCP server for ChatGPT and Claude (F2) |
  | `inbox.permaily.com` | MX for forwarded mail (F1) |

  Every host serves `/attestation?nonce=<hex>` and `/version`.
- **permaily-host** (`cmd/permaily-host`): forwards TCP 443 to vsock and exposes an allowlisted HTTP CONNECT proxy on vsock 8000 for the enclave's outbound TLS.
- **permaily-verify** (`cmd/permaily-verify`, `pkg/attestation`): verifier CLI and library.
- **Build** (`flake.nix`, `nix/`): Nix with [monzo/aws-nitro-util](https://github.com/monzo/aws-nitro-util) and AWS's prebuilt kernel/init blobs; PCR0 is identical from x86_64 and arm64 builders.
- **Infrastructure** (`deploy/`): OpenTofu; one Graviton `c7g.large` in `us-east-2` (enclave: 1 vCPU, 2 GiB), Cloudflare DNS (DNS only), S3 for deploy artifacts, SSM for administration.

The landing page at `permaily.com` (apex) is a separate project hosted on Amplify and is not part of the verifiable surface. Rule for F2: session cookies on `app.` use the `__Host-` prefix (no `Domain` attribute) so the apex cannot read or overwrite them.

## Technology choices

| Area | Choice |
|---|---|
| Language | Go, static binaries, no CGO |
| Image build | Nix + monzo/aws-nitro-util, AWS prebuilt blobs (aws-nitro-enclaves-cli v1.5.2) |
| TLS | ACME TLS-ALPN-01 inside the enclave (`golang.org/x/crypto/acme`), Let's Encrypt |
| Attestation (enclave) | `hf/nsm` |
| Attestation (verifier) | own implementation on `fxamacker/cbor` and the standard library |
| vsock | `mdlayher/vsock` |
| SPA | Vite + React + TypeScript with bun |
| IaC | OpenTofu (`aws`, `cloudflare` providers) |
| Releases | GitHub Actions, build provenance attestations (Sigstore) |
| MCP (F2) | `modelcontextprotocol/go-sdk` |
| Mail (F1) | `emersion/go-smtp`, `go-message`, `go-msgauth` |
