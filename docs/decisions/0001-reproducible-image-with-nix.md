# 0001 Build the enclave image reproducibly with Nix

Status: Accepted. Verified in CI on 2026-11-09 (x86_64 and arm64 builds identical).
Date: 2026-11-08

## Context

Verification means anyone rebuilds the enclave image (EIF) from source and gets the same PCR0 as the running enclave. `nitro-cli build-enclave` timestamps the image, so it is not reproducible.

## Options

1. **Nix + monzo/aws-nitro-util**: Nix flake that builds EIFs bit-for-bit; uses AWS's `eif_build` library; supports aarch64 and bringing your own kernel. Active (last commit May 2026).
2. **StageX**: full-source bootstrapped, container-native toolchain used by Turnkey. arm64 support and an aarch64 `eif_build` package could not be confirmed.
3. **kaniko + nitro-cli in Docker** (richardfan1126 action): reproducible with effort, but depends on Docker and Amazon Linux images.

## Decision

Option 1, with AWS's prebuilt kernel, init and nsm.ko blobs from aws-nitro-enclaves-cli v1.5.2 (pinned by hash) and a flat rootfs that holds only the static `permaily-enclave` binary.

## Consequences

- Verifiers need Nix or Docker (`scripts/verify.sh` handles both).
- Trusting AWS's prebuilt kernel is in scope; a source-built kernel is possible later.
- monzo/aws-nitro-util has low activity; it is small enough to fork or vendor.

## To verify

- A verifier on macOS (Apple Silicon, Docker) reproduces the release PCRs.
