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

Supported build systems: `x86_64-linux` and `aarch64-linux`. Without a local Nix, run it in Docker (`nixos/nix`), as `scripts/verify.sh` and `scripts/release-image.sh` do.

## Why the image is reproducible

- **Pinned inputs**: `flake.lock` (nixpkgs, monzo/aws-nitro-util), `vendorHash` for Go modules, fixed-output hashes for `node_modules` (one per build platform, because some npm packages ship native binaries), and the hash of the AWS blobs.
- **Go** binaries are built with the official Go release from go.dev (pinned by hash), not nixpkgs' Go, which patches the standard library to embed `/nix/store` paths that differ per build platform. They are static (`CGO_ENABLED=0`), built with `-trimpath`, no build ID, and cross-compiled by the Go toolchain through `GOARCH=arm64`, which gives identical output regardless of the host architecture.
- **SPA**: Vite output is content-hashed; no source maps or timestamps.
- **Mail templates**: the HTML of the mail is committed in `internal/domain/mail/templates` and embedded with `go:embed`, so the Go build reads files of the repository and runs nothing to make them. `packages/mail` (React Email, its own `bun.lock`) writes them at authoring time, and CI checks they are what it makes (ADR 0025).
- **Image**: AWS prebuilt kernel, init and nsm.ko for aarch64; a flat rootfs with only `/permaily-enclave`; cpio archives with fixed timestamps and ownership; `eif_build` with a fixed build time.

CI builds the image on x86_64 and arm64 runners and fails if any byte differs. Locally, `nix build .#eif --rebuild` checks determinism on one machine.

## Third-party programs in the image

The image contains, unmodified, AWS's kernel and `nsm.ko` (GPL-2.0) and AWS's `init`, which links the GNU C Library (LGPL-2.1) statically. Permaily does not distribute the image ([ADR 0023](decisions/0023-releases-publish-the-measurements-not-the-image.md)): a release publishes its measurements, and whoever needs the image builds it, getting those programs from AWS.

- `nix/aws-blobs.nix` pins the blobs (aws-nitro-enclaves-cli `v1.5.2`, `blobs/aarch64`).
- `nix/aws-blobs.json` is the inventory: each program, where it is in the image, the SHA-256 of its binary, its license, where its source is and how sure that is (`exact`, `aws-stated`, `unavailable`). `scripts/third-party-notices.ts` takes the image's AWS entries from it.
- `bun scripts/image-inventory.ts <permaily.eif>...` checks an image against the inventory: the kernel and every file of the boot ramdisk have the SHA-256 it gives, and nothing in the image is unaccounted for besides Permaily's entry point, environment and root filesystem. CI runs it on the production and staging images it builds.

Updating the AWS blobs means updating `aws-blobs.json` in the same change, with where the new binaries' source is.

TODO: build the kernel, `nsm.ko` and `init` from source in the flake (ADR 0023, option 2), which would let a release publish the image again.

## Updating dependencies

- Go: `go get ...`, `go mod tidy`, then update `vendorHash` in `nix/go.nix` (Nix prints the expected hash).
- SPA: `cd web && bun add --exact ...`, then update both hashes in `nix/web.nix`. The x86_64 hash comes from a local build; the aarch64 one from the CI failure message.
- Mail tooling: `cd packages/mail && bun add --exact ...` (versions at least two weeks old), then `bun run build` and `bun run check`. Nothing in Nix depends on it; if the templates change, so does PCR0.
- nixpkgs: `nix flake update`. If the bun version changes, update the `web` job in `.github/workflows/ci.yml` and regenerate `bun.lock` with that version.

## Local development

```sh
cd web && bun install && bun run build && cd ..
cp -R web/dist/. internal/webui/dist/
go run -tags dev ./cmd/permaily-enclave -port 8443
curl -k --resolve api.permaily.com:8443:127.0.0.1 https://api.permaily.com:8443/
```

Dev mode uses TCP, a self-signed certificate and a fake attester. Its documents never verify against the AWS root.
