# AGENTS.md

Permaily runs an email access broker inside an AWS Nitro Enclave whose image anyone can rebuild and verify. Every change must keep the enclave image reproducible and the verification chain intact.

## Read first

- [docs/architecture.md](docs/architecture.md): components and technology choices
- [docs/plan.md](docs/plan.md): phases and the design of planned work
- [docs/design.md](docs/design.md): visual design (tokens, typography, design language)
- [docs/security.md](docs/security.md): what is trusted and what is not; read before touching networking, TLS, attestation or the host
- [docs/build.md](docs/build.md): reproducible build rules and local development commands
- [docs/verification.md](docs/verification.md): the verification protocol

## Layers (each has its own AGENTS.md)

- [internal/AGENTS.md](internal/AGENTS.md): enclave service and host proxy (Go)
- [pkg/attestation/AGENTS.md](pkg/attestation/AGENTS.md): verifier library and CLI
- [web/AGENTS.md](web/AGENTS.md): web app (Vite, React, Tailwind, bun); screens in [docs/web.md](docs/web.md)
- [nix/AGENTS.md](nix/AGENTS.md): reproducible image build
- [deploy/AGENTS.md](deploy/AGENTS.md): OpenTofu, host setup, deployment

## Commands

```sh
go test ./...                       # unit and local end-to-end tests
go vet ./... && go vet -tags dev ./...
nix build .#eif                     # enclave image + pcrs.json (any Linux, x86_64 or arm64)
cd web && bun install && bun run build && bun run lint
```

## Rules

- The enclave image must stay byte-for-byte reproducible on x86_64 and arm64 builders. CI compares both; never merge a change that breaks it.
- Anything compiled into the enclave is covered by PCR0. Runtime configuration must not come from the parent instance; bake it into the image.
- The parent instance is untrusted. Do not add logic there that could read or alter user data.
- Use AWS only with an explicit profile (`AWS_PROFILE=permaily`).
- Commits: lowercase English Conventional Commits.
- Keep this file and the per-layer AGENTS.md files short; put details in `docs/`.
