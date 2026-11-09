# Decisions

Architecture Decision Records. Each one records the context, the options considered, the decision, its consequences and what remains to verify. They are never deleted: a reversed decision gets a new ADR that supersedes the old one. When a decision needed a prototype, it lives in `spikes/<name>/` with its own readme and tests, is never deployed or imported, and the ADR links to it.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-reproducible-image-with-nix.md) | Build the enclave image reproducibly with Nix and monzo/aws-nitro-util | Accepted |
| [0002](0002-tls-inside-the-enclave.md) | Terminate TLS inside the enclave with ACME TLS-ALPN-01 | Accepted, to verify in production |
| [0003](0003-official-go-toolchain.md) | Build Go with the official toolchain, not nixpkgs' Go | Accepted |
| [0004](0004-opentofu-and-region.md) | OpenTofu for infrastructure, AWS us-east-2, Cloudflare DNS | Accepted |
| [0005](0005-monorepo-with-the-site.md) | One repository for the service, the web app and the public site | Accepted |
| [0006](0006-personal-use-license.md) | Source-available personal use license with a CLA | Accepted, pending legal review |
| [0007](0007-oauth-server.md) | OAuth authorization server for apps and MCP clients | Proposed, needs a spike |
| [0008](0008-org-community-health-files.md) | Community health files live in the organization's .github repository | Accepted |
