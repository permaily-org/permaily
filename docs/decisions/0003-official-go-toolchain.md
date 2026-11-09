# 0003 Build Go with the official toolchain

Status: Accepted. Verified in CI on 2026-11-09.
Date: 2026-11-09

## Context

With nixpkgs' Go, the enclave binary differed between x86_64 (cross-compiled) and arm64 (native) builders, so PCR0 depended on the build machine. CI's per-component hashes showed the web app identical and the Go binary different. The binary contained `/nix/store/...-tzdata`, `...-iana-etc` and `...-mailcap` paths: nixpkgs patches the standard library to embed them, and the store paths differ per build platform.

## Options

1. Override nixpkgs' Go to drop those patches.
2. Use the official Go release from go.dev, pinned by hash per build platform.
3. Build only on arm64 (emulated on x86_64).

## Decision

Option 2 (`nix/go.nix`): the official toolchain, `CGO_ENABLED=0`, `-trimpath`, empty build ID, cross-compiled with `GOARCH`, and modules vendored by a fixed-output derivation.

## Consequences

- The Go version is pinned in two places (`go.mod` and `nix/go.nix`); upgrades update both.
- The output no longer contains any `/nix/store` path (checked when it was introduced).
