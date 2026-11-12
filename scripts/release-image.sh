#!/usr/bin/env bash
# Builds the enclave image of a release (or of a staging image) and checks that
# it is the image whose measurements were published with build provenance.
# Releases publish the measurements, not the image (ADR 0023): this is how the
# image to deploy is made.
#
#   scripts/release-image.sh <tag> <dir>                 # a release, e.g. v0.10.0
#   scripts/release-image.sh --staging <run id> <dir>    # a run of the staging-image workflow
#
# <dir> must be new or empty. The script leaves there what scripts/deploy.sh
# takes: pcrs.json and permaily-host-linux-arm64, downloaded, with their build
# provenance checked as scripts/verify.sh does; and permaily.eif, built from
# the commit of the tag (or of the run) and refused unless its measurements are
# exactly that pcrs.json. Nothing is deployed.
#
# Needs gh and jq, and Nix with flakes or Docker (the build then runs in the
# nixos/nix image). x86_64 and arm64 build the same bytes; CI checks both.
#
# Environment: PERMAILY_REPO, the repository (default permaily-org/permaily).
set -euo pipefail

usage() { sed -n '2,19p' "$0"; exit 2; }
staging=""
[[ ${1:-} == --staging ]] && { staging=1; shift; }
[[ $# -eq 2 ]] || usage
src=$1 dir=$2
repo=${PERMAILY_REPO:-permaily-org/permaily}

mkdir -p "$dir"
[[ -z $(ls -A "$dir") ]] || { echo "$dir is not empty" >&2; exit 1; }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# What was published, and the build provenance of each file.
if [[ -n $staging ]]; then
  read -r workflow branch sha conclusion < <(gh run view "$src" --repo "$repo" \
    --json workflowName,headBranch,headSha,conclusion -q '"\(.workflowName) \(.headBranch) \(.headSha) \(.conclusion)"')
  [[ $workflow == staging-image && $branch == staging/* && $conclusion == success ]] ||
    { echo "run $src is $workflow on $branch ($conclusion), not a staging image" >&2; exit 1; }
  gh run download "$src" --repo "$repo" --name staging-image --dir "$work/published"
  signer=(--signer-workflow "$repo/.github/workflows/staging-image.yml" --source-ref "refs/heads/$branch")
  flake="git+https://github.com/$repo?ref=refs/heads/$branch&rev=$sha#eif-staging"
else
  gh release download "$src" --repo "$repo" --pattern pcrs.json --pattern permaily-host-linux-arm64 --dir "$work/published"
  signer=(--signer-workflow "$repo/.github/workflows/release.yml" --source-ref refs/heads/main)
  flake="git+https://github.com/$repo?ref=refs/tags/$src#eif"
fi
for f in pcrs.json permaily-host-linux-arm64; do
  gh attestation verify "$work/published/$f" --repo "$repo" "${signer[@]}" >/dev/null
  echo "Build provenance of $f verified (Sigstore)."
done

# The image, built from the same commit.
echo "Building $flake"
mkdir "$work/built"
if command -v nix >/dev/null; then
  nix --extra-experimental-features 'nix-command flakes' build "$flake" --out-link "$work/eif"
  cp "$work/eif/permaily.eif" "$work/eif/pcrs.json" "$work/built/"
elif command -v docker >/dev/null; then
  docker run --rm -v "$work/built:/out" -e NIX_CONFIG='experimental-features = nix-command flakes' nixos/nix:latest \
    sh -c "nix build '$flake' --out-link /tmp/eif && cp /tmp/eif/permaily.eif /tmp/eif/pcrs.json /out/ && chmod a+rw /out/*"
else
  echo "need nix or docker" >&2; exit 1
fi

if ! cmp -s <(jq -S . "$work/built/pcrs.json") <(jq -S . "$work/published/pcrs.json"); then
  echo "REFUSED: the image built from $src has other measurements than the published pcrs.json" >&2
  echo "built:" >&2; cat "$work/built/pcrs.json" >&2
  echo "published:" >&2; cat "$work/published/pcrs.json" >&2
  exit 1
fi
install -m 0644 "$work/built/permaily.eif" "$work/published/pcrs.json" "$dir/"
install -m 0755 "$work/published/permaily-host-linux-arm64" "$dir/"
echo "The image built from $src has the published measurements (PCR0 $(jq -r .PCR0 "$dir/pcrs.json"))."
echo "$dir holds permaily.eif, pcrs.json and permaily-host-linux-arm64 for scripts/deploy.sh."
