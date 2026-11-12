# nix/AGENTS.md: reproducible build

Builds the SPA (`web.nix`), the Go binaries (`go.nix`), the enclave image with its PCRs (`eif.nix`) and the archive of the source of the third-party programs in it (`gpl-source.nix`, published with each release). Background and rules: [../docs/build.md](../docs/build.md).

## Rules

- PCR0 must not depend on the build machine. Keep: the official Go toolchain (nixpkgs' Go embeds platform-specific store paths), Go cross-compilation through `GOARCH` (not a Nix cross toolchain), AWS prebuilt kernel/init/nsm.ko blobs, a flat rootfs without `/nix/store` paths.
- Every input is pinned: `flake.lock`, `vendorHash`, the `node_modules` output hashes, the AWS blobs hash (`aws-blobs.nix`), the source inputs of `gpl-source.nix`. When one changes, update the hash and say why in the commit.
- Every third-party binary in the image needs its source in `gpl-source.nix`: changing the AWS blobs, or adding one to the image, means updating `gpl-source/manifest.json` and `gpl-source/about.md` with where its source is and how certain that is. The build of `.#gpl-source` fails on a blob the manifest does not describe, and the release fails on an image the archive does not account for (`scripts/gpl-source.ts`).
- Entry point arguments and environment are part of the image; AWS init reads one item per line, so an empty value is left out with its flag.
- `.#eif-staging` is the same code with the `staging` object of `deploy/enclave.json` applied: its own storage and a release policy that only accepts images built by the `staging-image` workflow. Keep everything that separates it from production in that object.
- Verify locally with `nix build .#eif --rebuild`; CI checks x86_64 against arm64.
