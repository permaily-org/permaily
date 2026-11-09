# Releasing

## Branches

- `develop` is the default branch. Work happens on branches from `develop`; pull requests into it are squash-merged, so the PR title (lowercase Conventional Commit, checked by `.github/workflows/pr.yml`) becomes the commit.
- `main` only receives merge commits from `develop` (also checked by `pr.yml`). Nobody tags or edits versions by hand.

## What a release does

On every push to `main`, `.github/workflows/release.yml`:

1. Builds the enclave image, `permaily-host` and `permaily-verify` on x86_64 and arm64, and fails if the image or host binary differ.
2. Writes `SHA256SUMS` and signs every file with GitHub build provenance (Sigstore), verifiable with `gh attestation verify <file> --repo permaily-org/permaily`.
3. Runs semantic-release: computes the version from the commits since the last tag (`feat` minor, `fix` patch; breaking changes are minor below 1.0), creates the tag and the GitHub release with the notes, `permaily.eif`, `pcrs.json`, the binaries and `SHA256SUMS`.
4. Merges `main` back into `develop`.

The tag points at the built commit; there is no release commit. So `scripts/verify.sh <tag>` rebuilds exactly the published PCRs.

## Depends on

- Branch rulesets on `develop` and `main` (required checks, no direct pushes). Once they block pushes, the `sync-develop` job needs a deploy key in the `RELEASE_DEPLOY_KEY` secret allowed to bypass them.
- Deploying a release is manual for now ([operations.md](operations.md)), because the AWS account cannot run the instance yet.
