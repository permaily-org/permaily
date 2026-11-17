#!/usr/bin/env bash
# Checks deploy/tofu without AWS or Cloudflare: `tofu fmt -check`, then
# `tofu validate` twice, in this checkout and in the layout the hourly drift
# check plans (deploy/drift/drift.sh): only deploy/, without its dot files,
# and the lock file put back. A configuration that reads a file outside
# deploy/ passes the first and fails the second, and with it the drift check.
#
#   scripts/tofu-validate.sh
#
# Needs tofu (TOFU, default tofu on PATH; nix develop has it), git, and the
# network to download the providers the lock file names, which it checks. It
# writes nothing in the checkout: tofu's data goes to a temporary directory.
set -euo pipefail

tofu=${TOFU:-tofu}
repo=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export TF_PLUGIN_CACHE_DIR=$work/plugins TF_IN_AUTOMATION=1 TF_INPUT=0
mkdir "$TF_PLUGIN_CACHE_DIR"

# validate DIR NAME: init without the backend, the lock file read-only, then validate.
validate() {
  echo "== tofu validate, $2 =="
  TF_DATA_DIR=$work/data-$2 "$tofu" -chdir="$1" init -backend=false -lockfile=readonly -no-color >"$work/init-$2.txt" 2>&1 ||
    { cat "$work/init-$2.txt" >&2; echo "tofu init failed, $2" >&2; return 1; }
  TF_DATA_DIR=$work/data-$2 "$tofu" -chdir="$1" validate -no-color
}

"$tofu" fmt -check -recursive -diff "$repo/deploy"

validate "$repo/deploy/tofu" checkout

# What `tofu init -from-module` of deploy/ leaves: the files git tracks (and
# new ones it does not ignore), as they are here, but none whose path has a
# component starting with a dot; drift.sh then downloads the lock file alone.
drift=$work/drift/deploy
mkdir -p "$drift"
(
  cd "$repo"
  git ls-files -z --cached --others --exclude-standard -- deploy |
    while IFS= read -r -d '' f; do
      [[ -e $f && /${f#deploy/} != */.* ]] || continue
      mkdir -p "$drift/$(dirname "${f#deploy/}")"
      cp -p "$f" "$drift/${f#deploy/}"
    done
)
cp "$repo/deploy/tofu/.terraform.lock.hcl" "$drift/tofu/.terraform.lock.hcl"
validate "$drift/tofu" "drift-layout"
