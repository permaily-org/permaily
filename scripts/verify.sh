#!/usr/bin/env bash
# Reproduces the enclave image of a git ref and verifies the live service
# against it. Needs either Nix (with flakes) or Docker.
#
#   scripts/verify.sh v0.1.0                  # verify the production deployment
#   scripts/verify.sh v0.1.0 -json            # extra flags go to permaily-verify
#
# With Docker only, the build runs in the nixos/nix image. The first build
# downloads a lot and takes several minutes.
set -euo pipefail

[[ $# -ge 1 ]] || { sed -n '2,10p' "$0"; exit 2; }
ref=$1; shift
repo_url=${PERMAILY_REPO:-https://github.com/permaily-org/permaily}
flake="git+$repo_url?ref=refs/tags/$ref"
[[ $ref =~ ^[0-9a-f]{40}$ ]] && flake="git+$repo_url?rev=$ref"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

if command -v nix >/dev/null; then
  nix --extra-experimental-features 'nix-command flakes' build "$flake#eif" --out-link "$work/eif"
  nix --extra-experimental-features 'nix-command flakes' build "$flake#permaily-verify" --out-link "$work/verify"
  cp "$work/eif/pcrs.json" "$work/pcrs.json"
  verify="$work/verify/bin/permaily-verify"
elif command -v docker >/dev/null; then
  docker run --rm -v "$work:/out" -e NIX_CONFIG='experimental-features = nix-command flakes' nixos/nix:latest \
    sh -c "nix build '$flake#eif' --out-link /tmp/eif && cp /tmp/eif/pcrs.json /out/pcrs.json &&
           nix build '$flake#permaily-verify' --out-link /tmp/v && cp /tmp/v/bin/permaily-verify /out/ && chmod -R a+rw /out"
  verify="$work/permaily-verify"
  # The container builds a linux binary; on other systems run it through Go.
  if [[ $(uname -s) != Linux ]]; then
    verify="go run github.com/permaily-org/permaily/cmd/permaily-verify@$ref"
  fi
else
  echo "need nix or docker" >&2; exit 1
fi

echo "Reproduced PCRs for $ref:"; cat "$work/pcrs.json"; echo
$verify -pcrs "$work/pcrs.json" "$@"
