# 0023 Releases publish the image's measurements, not the image

Status: Accepted
Date: 2026-11-12

## Context

The enclave image (`permaily.eif`) contains, unmodified, four programs Permaily did not write, all prebuilt by AWS and taken from `blobs/aarch64` of aws-nitro-enclaves-cli `v1.5.2` (`nix/aws-blobs.nix`): a Linux kernel and the `nsm.ko` driver (GPL-2.0), and an `init` (Apache-2.0) linked statically with the GNU C Library (LGPL-2.1-or-later). `nix/aws-blobs.json` lists each with the SHA-256 of its binary.

Releases v0.1.0 to v0.10.0 attached the image as a public asset, which is distributing those binaries. Whoever distributes them must accompany them with their complete corresponding source, or with a written offer of it valid for three years (GPL-2.0 section 3, LGPL-2.1 section 6); pointing to AWS's repositories does not do it (GPL-2.0 3(c) covers only noncommercial redistribution of an offer received).

What AWS publishes, read on 2026-11-12:

- `nsm.ko`: its `srcversion` is the MD4 digest `modpost` takes of the module's source, and it matches `nsm-driver/nsm.c` of aws-nitro-enclaves-sdk-bootstrap at `ed24913` (unchanged since `9a1a825`). The source is certain.
- Kernel: the binary is `4.14.256-209.484.amzn2.aarch64` (SHA-256 `03808bbf…2ec9b9`), built by Amazon's package build system (`mockbuild`) on 2022-01-11. Its configuration is published, byte for byte, in aws-nitro-enclaves-sdk-bootstrap at `ed24913`. Its source is not: the Amazon Linux 2 source repository has `kernel-4.14.256-197.484.amzn2.src.rpm` and no `209.484`, and the bootstrap repository's own build downloads `linux-4.14.204`.
- `init` (SHA-256 `c478b57a…a0dc590d`): AWS replaced it 25 minutes after merging aws-nitro-enclaves-sdk-bootstrap `203242d` (tag `v1.0.0`) and cited that change. It was built with a Red Hat GCC 11.4.1 and is not reproducible from it, so its source is AWS's word. Which release of the GNU C Library is inside is not recorded, and AWS does not say.

Nobody outside needs the image file. Whoever verifies rebuilds the image from the source, because the build is reproducible (and the rebuild is what counts, `docs/verification.md`); a running enclave hands the root secret to the next release on the build provenance of its `pcrs.json` (`pkg/provenance` checks that the statement names that file's SHA-256, nothing else); the only user of the released image was the operator, to deploy it.

## Options

1. **Attach what exists and go on.** A source archive with each release: the configuration, `nsm.c`, `init.c`, the licence texts, and a manifest saying what is missing. It cannot hold the kernel's or the C library's source, which Permaily does not have, so every release would still fall short of the licence, and a written offer would promise what Permaily cannot give.
2. **Build the kernel, `nsm.ko` and `init` from source** in the flake (aws-nitro-enclaves-sdk-bootstrap `v6.6.79` builds Linux 6.6.79 with Nix; `init` against musl, or monzo/aws-nitro-util's Go `init`, removes the LGPL altogether). The source of every binary is then whatever Nix built it from. It costs a kernel build on each architecture in CI, evidence that the cross (x86_64) and native (arm64) builds give the same bytes, a boot test on a Graviton enclave, and new measurements.
3. **Stop distributing the image.** Releases publish the measurements and their build provenance, the verifier and the host program, and not the image. Whoever needs the image builds it, and gets AWS's programs from AWS, under AWS's licences.

## Decision

3, decided by the maintainer on 2026-11-12. It ends the distribution without changing a byte of the image or its measurements, takes nothing from whoever verifies (who rebuilds anyway), and asks nothing new of the running enclave.

- `release.yml` still builds the image on x86_64 and arm64 and requires the two to be identical, comparing the image's checksums and `pcrs.json`; it attaches `pcrs.json`, `permaily-host-linux-arm64`, `permaily-verify-linux-*`, `third-party-notices.md` and `SHA256SUMS`, all under build provenance, and not the image. No workflow hands the image to another job or uploads it as an artifact; `staging-image.yml` does the same for staging images.
- The operator builds the image to deploy: `scripts/release-image.sh <tag> <dir>` (or `--staging <run id>`) downloads the release's `pcrs.json` and host program, checks their build provenance as `scripts/verify.sh` does, builds the image of that commit with Nix (or in Docker), and refuses unless its measurements are that `pcrs.json`.
- `nix/aws-blobs.json` stays as the inventory of the third-party programs in the image, with where each one's source is and how sure that is; CI checks every image it builds against it (`scripts/image-inventory.ts`), so the blobs cannot change without the inventory saying what they are.
- `third-party-notices.md` says the image contains those programs and that Permaily does not distribute it.

## Consequences

- The eleven releases already published (v0.1.0 to v0.10.0) still carry `permaily.eif`, and with it the kernel, `nsm.ko` and `init` without their source. Removing the asset from them is prepared and not done; their `pcrs.json`, its provenance and the rebuild are unaffected, and an older release can still be deployed by building it with `scripts/release-image.sh`.
- Deploying needs a build: Nix, or Docker with the `nixos/nix` image, on x86_64 or arm64 Linux (or Docker elsewhere). Rebuilding an old release depends on its pinned inputs staying fetchable, as verifying it already did.
- AWS can be asked to publish the source of `4.14.256-209.484.amzn2` and of the C library in `init`. If it does, option 1 becomes complete.
- Option 2 remains the way to distribute the image again.
