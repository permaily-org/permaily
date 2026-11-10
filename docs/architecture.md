# Architecture

## Goal

Apps such as travel assistants ask for full Gmail access to read a few messages. Permaily sits in between: the user connects their mailbox once, and each app or AI agent receives only the subset of messages the user authorized. Rules are enforced on every delivery, with an audit log and revocation.

What sets Permaily apart is verifiability: the whole service runs inside an AWS Nitro Enclave built reproducibly from this repository, so anyone can check that the code running is the published code. See [verification.md](verification.md) and [threat-model.md](threat-model.md). Phases and planned work are in [plan.md](plan.md); the visual design is in [design.md](design.md).

## Components (F0 and F1)

```
client / verifier ──TLS──▶ EIP:443 ─▶ permaily-host (parent EC2, untrusted) ─vsock 443─▶ permaily-enclave
sending MTA ─STARTTLS───▶ EIP:25  ─▶ permaily-host ──────────────────────────────vsock 25──▶  ├─ ACME TLS-ALPN-01, key only in RAM
                                                                                            ├─ SMTP → inbox: parse, DKIM, seal, store
                         permaily-host vsock 8000 (CONNECT allowlist) ◀─────────────────────┤  egress: Let's Encrypt, KMS, DynamoDB, S3, DoH
                         permaily-host vsock 8002 (instance role credentials) ◀─────────────┤  AWS credentials (useless without attestation)
                         permaily-host vsock 8003 (log lines, one way) ◀──────────────────────┤  logs (public by rule)
                                                                                            └─ /dev/nsm → attestation(nonce, sha256(SPKI)); KMS Recipient
```

- **permaily-enclave** (`cmd/permaily-enclave`): one Go binary. Terminates TLS, obtains one certificate for all hosts, dispatches by Host:

  | Host | Serves |
  |---|---|
  | `app.permaily.com` | web app (see [web.md](web.md)), embedded with `go:embed` |
  | `api.permaily.com` | `/v1/mailbox` for the F1 test mailbox (token); API for integrated apps in F2 |
  | `mcp.permaily.com` | remote MCP server for ChatGPT and Claude (F2) |
  | `inbox.permaily.com` | MX (SMTP on port 25 with STARTTLS) for forwarded mail (F1) |
  | `mta-sts.inbox.permaily.com` | MTA-STS policy for the MX (F1) |

  Every host serves `/attestation?nonce=<hex>`, `/status` (the KMS key that locks the root secret, and the TLS identity) and `/version`.
- **Mail (F1)**: `internal/smtpd` (MX), `internal/inbox` (parse, Gmail confirmation codes, seal, serve), `internal/msgauth` (DKIM over DoH), `internal/keys` (the root secret that every data key is wrapped under, its handoff to the next release, KMS Recipient and CMS), `internal/envelope` (AES-256-GCM with key commitment), `internal/store` (DynamoDB and S3). Design: [plan.md](plan.md), ADR 0008 and 0009; data: [data.md](data.md).
- **permaily-host** (`cmd/permaily-host`): forwards TCP 443 and 25 to vsock, exposes an allowlisted HTTP CONNECT proxy on vsock 8000 for the enclave's outbound TLS, and serves the instance role credentials on vsock 8002.
- **permaily-verify** (`cmd/permaily-verify`, `pkg/attestation`, `pkg/kmspolicy`, `pkg/certwatch`): verifier CLI and libraries. It checks the image, the TLS key, that the KMS key locking the root secret belongs to that image alone for good, how mail gets in, and whether a certificate for the hosts exists outside the enclave.
- **Releases and the root secret** (`pkg/provenance`, ADR 0013): the running enclave hands the root secret only to an image whose measurements were published by the release workflow, which it checks offline from the release's Sigstore bundle. Nothing in AWS is edited to let a release in.
- **Build** (`flake.nix`, `nix/`): Nix with [monzo/aws-nitro-util](https://github.com/monzo/aws-nitro-util) and AWS's prebuilt kernel/init blobs; PCR0 is identical from x86_64 and arm64 builders.
- **Infrastructure** (`deploy/`): OpenTofu; one Graviton `c7g.large` in `us-east-2` (enclave: 1 vCPU, 2 GiB), Cloudflare DNS (DNS only), S3 for deploy artifacts, SSM for administration; for mail, a DynamoDB table and an S3 bucket (`mail.tf`), one KMS key per release (`releases.tf`) and the storage of the staging deployment (`staging.tf`). The image's runtime configuration is `deploy/enclave.json`.

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
