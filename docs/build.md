# Build

## Outputs

| Command | Output |
|---|---|
| `nix build .#eif` | `permaily.eif`, `pcrs.json`, `SHA256SUMS` (production, Let's Encrypt production) |
| `nix build .#eif-staging` | same, against Let's Encrypt staging |
| `nix build .#permaily-host` | host proxy, linux/arm64 |
| `nix build .#permaily-verify` | verifier for the build machine |
| `nix build .#web` | SPA `dist/` |
| `nix develop` | shell with Go, bun, OpenTofu, AWS CLI, jq |

Supported build systems: `x86_64-linux` and `aarch64-linux`. Without a local Nix, run it in Docker (`nixos/nix`), as `scripts/verify.sh` does.

## Why the image is reproducible

- **Pinned inputs**: `flake.lock` (nixpkgs, monzo/aws-nitro-util), `vendorHash` for Go modules, fixed-output hashes for `node_modules` (one per build platform, because some npm packages ship native binaries), and the hash of the AWS blobs.
- **Go** binaries are static (`CGO_ENABLED=0`), built with `-trimpath`, no build ID, and cross-compiled by the Go toolchain through `GOARCH=arm64`. Go produces identical output regardless of the host architecture.
- **SPA**: Vite output is content-hashed; no source maps or timestamps.
- **Image**: AWS prebuilt kernel, init and nsm.ko for aarch64; a flat rootfs with only `/permaily-enclave`; cpio archives with fixed timestamps and ownership; `eif_build` with a fixed build time.

CI builds the image on x86_64 and arm64 runners and fails if any byte differs. Locally, `nix build .#eif --rebuild` checks determinism on one machine.

## Updating dependencies

- Go: `go get ...`, `go mod tidy`, then update `vendorHash` in `nix/go.nix` (Nix prints the expected hash).
- SPA: `cd web && bun add --exact ...`, then update both hashes in `nix/web.nix`. The x86_64 hash comes from a local build; the aarch64 one from the CI failure message.
- nixpkgs: `nix flake update`. If the bun version changes, update the `web` job in `.github/workflows/ci.yml` and regenerate `bun.lock` with that version.

## Local development

```sh
cd web && bun install && bun run build && cd ..
cp -R web/dist/. internal/webui/dist/
go run -tags dev ./cmd/permaily-enclave -port 8443
curl -k --resolve api.permaily.com:8443:127.0.0.1 https://api.permaily.com:8443/
```

Dev mode uses TCP, a self-signed certificate and a fake attester. Its documents never verify against the AWS root.
