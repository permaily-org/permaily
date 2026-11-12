# nix/AGENTS.md: reproducible build

Builds the SPA (`web.nix`), the Go binaries (`go.nix`) and the enclave image with its PCRs (`eif.nix`), from AWS's blobs (`aws-blobs.nix`). Background and rules: [../docs/build.md](../docs/build.md).

## Rules

- PCR0 must not depend on the build machine. Keep: the official Go toolchain (nixpkgs' Go embeds platform-specific store paths), Go cross-compilation through `GOARCH` (not a Nix cross toolchain), AWS prebuilt kernel/init/nsm.ko blobs, a flat rootfs without `/nix/store` paths.
- Every input is pinned: `flake.lock`, `vendorHash`, the `node_modules` output hashes, the AWS blobs hash (`aws-blobs.nix`). When one changes, update the hash and say why in the commit.
- Every third-party binary in the image is described in `aws-blobs.json`: what it is, its SHA-256, its license, where its source is and how sure that is. Changing the AWS blobs, or adding a binary to the image, means updating it in the same change; CI fails on an image it does not account for (`scripts/image-inventory.ts`).
- The image is never a release asset nor a workflow artifact: it contains AWS's GPL kernel, whose source AWS does not publish (ADR 0023). Workflows compare and publish `pcrs.json` and checksums; whoever needs the image builds it (`scripts/release-image.sh`).
- Entry point arguments and environment are part of the image; AWS init reads one item per line, so an empty value is left out with its flag.
- `.#eif-staging` is the same code with the `staging` object of `deploy/enclave.json` applied: its own storage and a release policy that only accepts images built by the `staging-image` workflow. Keep everything that separates it from production in that object.
- Verify locally with `nix build .#eif --rebuild`; CI checks x86_64 against arm64.
