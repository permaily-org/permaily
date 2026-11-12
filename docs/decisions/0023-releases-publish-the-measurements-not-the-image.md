# 0022 Publish the source of the GPL parts of the image with each release

Status: Accepted (the archive and the gate); building the kernel and init from source is pending.
Date: 2026-11-12

## Context

`permaily.eif`, published with every release, contains AWS's prebuilt kernel, `nsm.ko` and `init` (`nix/aws-blobs.nix`: `blobs/aarch64` of aws-nitro-enclaves-cli `v1.5.2`). The kernel and `nsm.ko` are GPL-2.0; `init` is Apache-2.0 but is linked statically with the GNU C Library (LGPL-2.1-or-later). Whoever distributes those binaries must accompany them with their complete corresponding source, or with a written offer of it valid for three years (GPL-2.0 section 3, LGPL-2.1 section 6). Releases v0.1.0 to v0.10.0 only pointed to AWS's repositories, which a review found does not discharge it.

What AWS publishes, read on 2026-11-12:

- `nsm.ko`: its `srcversion` is the MD4 digest `modpost` takes of the module's source, and it matches `nsm-driver/nsm.c` of aws-nitro-enclaves-sdk-bootstrap at `ed24913` (and `9a1a825`, where the file last changed). The source is certain.
- Kernel: the binary is `4.14.256-209.484.amzn2.aarch64`, built by Amazon's package build system (`mockbuild`) on 2022-01-11. Its configuration is published, byte for byte, in aws-nitro-enclaves-sdk-bootstrap at `ed24913`. Its source is not: the Amazon Linux 2 source repository has `kernel-4.14.256-197.484.amzn2.src.rpm` and no `209.484`, and the bootstrap repository's own build downloads `linux-4.14.204`.
- `init`: AWS replaced it 25 minutes after merging aws-nitro-enclaves-sdk-bootstrap `203242d` (tag `v1.0.0`) and cited that change. It was built with a Red Hat GCC 11.4.1 and is not reproducible from it, so its source is AWS's word. Which release of the GNU C Library is inside is not recorded.

## Options

1. **Written offer (section 3(b)).** A promise, for three years after the last release, to send the source on request. It needs someone to act later, and it cannot be kept for the kernel and the C library, whose source Permaily does not have.
2. **Source archive with each release (section 3(a)).** Built by Nix from pinned inputs, attached next to the image, covered by the same provenance and checksums. Nobody has to act later.
3. **Build the kernel, `nsm.ko` and `init` from source** in the flake, so the source of every binary is whatever Nix built it from. It changes the image and its measurements, and the build has to stay identical on x86_64 and arm64.

## Decision

2 now, and 3 next.

- `nix build .#gpl-source` builds `permaily-gpl-source.tar.gz`: the GPL-2.0 and LGPL-2.1 texts, `manifest.json` (each program in the image, the SHA-256 of its binary, which files are its source, and `exact`, `aws-stated` or `unavailable`), a README saying how certain each source is and why, AWS's kernel configuration, and aws-nitro-enclaves-sdk-bootstrap at `ed24913` and `203242d`. The build fails when a blob is not the binary the manifest describes, so the blobs cannot be updated without updating the source.
- `release.yml` builds it on both architectures, requires the two to be identical, attaches it and `third-party-notices.md` to the release (both in `SHA256SUMS` and in the build provenance), and runs `scripts/gpl-source.ts --require-complete`: the archive must describe exactly the kernel and boot ramdisk files of the image being released, and every copyleft program in it must have its source. CI runs the same check without `--require-complete`, as a warning.
- No written offer: it would promise what Permaily cannot give.

## Consequences

- While the kernel and the C library in `init` are AWS's binaries, the release gate fails: no release is cut until option 3 lands, or AWS publishes the source of `4.14.256-209.484.amzn2` and of the C library it linked. Removing `--require-complete` from `release.yml` releases anyway, knowingly short of the license.
- Option 3 for the kernel and `nsm.ko`: aws-nitro-enclaves-sdk-bootstrap `v6.6.79` builds Linux 6.6.79 with Nix (its `kernel/kernel.nix`: upstream source, two patches, AWS's configuration, fixed build time and host). Building it in the flake costs a kernel build in CI on each architecture, the evidence that the x86_64 (cross) and arm64 (native) builds give the same bytes, a boot test on a Graviton enclave, and new PCRs (a release like any other). For `init`, a build from source against a C library Nix provides (musl, MIT, as the bootstrap repository's own Nix build does) or monzo/aws-nitro-util's Go `init` removes the LGPL obligation altogether.
- Releases v0.1.0 to v0.10.0 remain as published. The archive of this change describes their blobs too (they are the same `v1.5.2` blobs), so attaching it to them changes no image.
