# nix/AGENTS.md: reproducible build

Builds the SPA (`web.nix`), the Go binaries (`go.nix`) and the enclave image with its PCRs (`eif.nix`). Background and rules: [../docs/build.md](../docs/build.md).

## Rules

- PCR0 must not depend on the build machine. Keep: Go cross-compilation through `GOARCH` (not a Nix cross toolchain), AWS prebuilt kernel/init/nsm.ko blobs, a flat rootfs without `/nix/store` paths.
- Every input is pinned: `flake.lock`, `vendorHash`, the `node_modules` output hashes, the AWS blobs hash. When one changes, update the hash and say why in the commit.
- Entry point arguments and environment are part of the image; AWS init reads one item per line.
- Verify locally with `nix build .#eif --rebuild`; CI checks x86_64 against arm64.
