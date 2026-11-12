# AGENTS.md

Permaily runs an email access broker inside an AWS Nitro Enclave whose image anyone can rebuild and verify. Every change must keep the enclave image reproducible and the verification chain intact.

## Documents

| Document | Read it when |
|---|---|
| [docs/architecture.md](docs/architecture.md) | Before changing how components fit together |
| [docs/plan.md](docs/plan.md) | Before starting work from a later phase (F1+) |
| [docs/decisions/](docs/decisions/index.md) | Before revisiting a decision; add an ADR for a new one |
| [docs/threat-model.md](docs/threat-model.md) | Before touching networking, TLS, attestation, the host, auth or logging |
| [docs/verification.md](docs/verification.md) | Before changing `/attestation`, `/status`, `pkg/attestation`, `pkg/kmspolicy`, `pkg/certwatch`, `pkg/provenance`, the KMS key policy, CAA records or the build |
| [docs/build.md](docs/build.md) | Before changing dependencies, Nix or anything in the image |
| [docs/testing.md](docs/testing.md) | Before writing or changing tests |
| [docs/acceptance.md](docs/acceptance.md) | When a change adds evidence for a criterion (update its row) |
| [docs/releasing.md](docs/releasing.md) | Before merging into `main` or changing release workflows |
| [docs/deploy.md](docs/deploy.md), [docs/operations.md](docs/operations.md) | Before touching infrastructure or production |
| [docs/design.md](docs/design.md), [docs/web.md](docs/web.md), [docs/site.md](docs/site.md), [docs/accessibility.md](docs/accessibility.md) | Before changing the web app or the public site |
| [docs/data.md](docs/data.md), [docs/legal.md](docs/legal.md) | Before storing user data or changing legal pages |

Each layer has its own short AGENTS.md: [internal/](internal/AGENTS.md) (enclave service and host), [pkg/attestation/](pkg/attestation/AGENTS.md) (verifier), [web/](web/AGENTS.md) (web app), [site/](site/AGENTS.md) (public site), [packages/ui/](packages/ui/AGENTS.md) (shared tokens), [nix/](nix/AGENTS.md) (image build), [deploy/](deploy/AGENTS.md) (infrastructure).

## Commands

```sh
nix develop                 # go, golangci-lint, govulncheck, bun, node, opentofu, aws, jq
make check                  # every gate; must pass before a change is done
make fuzz                   # short fuzzing of the attestation parser
nix build .#eif             # enclave image + pcrs.json (x86_64 or arm64 Linux)
nix build .#gpl-source      # source of the GPL programs in the image, published with each release
bun run format              # Biome, whole repo
```

## Rules

- The enclave image must stay byte-for-byte reproducible on x86_64 and arm64 builders. CI compares both; never merge a change that breaks it.
- Anything compiled into the enclave is covered by PCR0. Runtime configuration must not come from the parent instance; it lives in `deploy/enclave.json` and is baked into the image.
- The parent instance is untrusted. Do not add logic there that could read or alter user data. `depguard` enforces part of this.
- Never log email content, addresses, tokens, cookies or codes.
- Every bug fix starts with a failing test. Coverage thresholds only go up.
- Use AWS only with an explicit profile (`AWS_PROFILE=permaily`, region `us-east-2`).
- Git: work on a branch from `develop`; PR titles are lowercase Conventional Commits (squash-merged); `main` only receives merges from `develop`. No agent attribution in commits or PRs.
- Files are lowercase except `AGENTS.md` and `CLAUDE.md`; there is one `readme.md`, at the root, written for customers. Markdown paragraphs stay on one line.

## Keeping these instructions current

Stale instructions are a bug. Before finishing a change, check:

1. Did a command, path or tool change? Update the commands above and the layer's AGENTS.md.
2. Did you make a decision someone could reverse later? Add an ADR in `docs/decisions/`.
3. Did you add evidence for an acceptance criterion? Update `docs/acceptance.md`.
4. Did you change what is trusted, logged or reachable? Update `docs/threat-model.md`.
5. Did a TODO get done or a new one appear? Update the TODO list in the relevant doc.
