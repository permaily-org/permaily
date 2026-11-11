# Releasing

## Branches

- `develop` is the default branch. Work happens on branches from `develop`; pull requests into it are squash-merged, so the PR title (lowercase Conventional Commit, checked by `.github/workflows/pr.yml`) becomes the commit.
- `main` only receives merge commits from `develop` (also checked by `pr.yml`). Nobody tags or edits versions by hand.

## How to release

1. Run **Actions > release-pr > Run workflow** (or `gh workflow run release-pr.yml --repo permaily-org/permaily`).
2. It opens (or updates) the pull request from `develop` into `main` with the changes since the last release and turns on auto-merge. The pull request is opened by the `permaily-release` app, so CI runs on it.
3. When CI passes, it merges with a merge commit and the release starts. Nothing else to do.

Opening the pull request by hand works too; it just needs auto-merge or a manual merge.

## What a release does

On every push to `main`, `.github/workflows/release.yml`:

1. Builds the enclave image, `permaily-host` and `permaily-verify` on x86_64 and arm64, and fails if the image or host binary differ.
2. Writes `SHA256SUMS` and signs every file with GitHub build provenance (Sigstore), verifiable with `gh attestation verify <file> --repo permaily-org/permaily --signer-workflow permaily-org/permaily/.github/workflows/release.yml --source-ref refs/heads/main` (without the last two flags it accepts what any workflow of the repository signed, a staging image included).
3. Checks that a running enclave would accept the release: the new verifier and the previous release's both run `-provenance` on the fresh bundle. If either refuses, no release is cut (docs/operations.md, "Sigstore changed").
4. Runs semantic-release: computes the version from the commits since the last tag (`feat` minor, `fix` patch; breaking changes are minor below 1.0), creates the tag and the GitHub release with the notes, `permaily.eif`, `pcrs.json`, the binaries and `SHA256SUMS`.

Versions start from `v0.0.0`, a tag on the repository's first commit, so the first release is 0.1.0 and the project stays below 1.0 until it is declared stable. `scripts/check-release-notes.mjs` (CI) renders sample notes with the configured preset, so an incompatible semantic-release or preset upgrade fails on the pull request.

After publishing, it fast-forwards `develop` to `main` and calls the Amplify release webhook so permaily.com rebuilds and its footer shows the new PCR0.

The tag points at the built commit; there is no release commit. So `scripts/verify.sh <tag>` rebuilds exactly the published PCRs.

## Depends on

- Branch rulesets on `develop` and `main` (required checks, pull requests only; admins can bypass).
- The `permaily-release` GitHub App (installed on this repository only; contents, workflows and pull requests write) is the only actor allowed to bypass the `develop` ruleset. `release.yml` uses it, through `RELEASE_APP_CLIENT_ID` (variable) and `RELEASE_APP_PRIVATE_KEY` (secret), to fast-forward `develop` to `main` after each release, so both branches point at the same commit.
- Deploying a release is manual ([operations.md](operations.md)).
