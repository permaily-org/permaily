#!/usr/bin/env bash
# A change to the trust root compiled into pkg/provenance changes who the
# enclave believes about releases, and the TUF root among those files is the
# anchor that every later update is checked against. So a change to them is
# accepted only when it is exactly an update that the code of the base branch
# takes: what Sigstore publishes today, each TUF root signed by the
# keyholders of the one before, starting from the anchor of the base.
#
#   scripts/check-trust-root-change.sh [base ref, default origin/develop]
#
# Does nothing when the files did not change. Needs git, go, curl and jq.
set -euo pipefail

cd "$(dirname "$0")/.."
base=${1:-origin/develop}
dir=pkg/provenance
files=("$dir/sigstore_trusted_root.json" "$dir/sigstore_tuf_root.json" "$dir/sigstore_tuf_targets.json")
if git diff --quiet "$base" -- "${files[@]}"; then
  echo "The compiled-in trust root is the one of $base."
  exit 0
fi

work=$(mktemp -d)
trap 'git worktree remove --force "$work/base" 2>/dev/null || true; rm -rf "$work"' EXIT
git worktree add -q --detach "$work/base" "$base"
(cd "$work/base" && go build -o "$work/verify" ./cmd/permaily-verify && scripts/sigstore-trust-root.sh --fetch "$work/tuf" >/dev/null)
"$work/verify" -trust-update "$work/tuf"

same() { cmp -s "$1" "$2" || { echo "$1 is not the file Sigstore publishes today ($3)" >&2; exit 1; }; }
same "${files[0]}" "$work/tuf/trusted_root.json" trusted_root.json
same "${files[2]}" "$work/tuf/targets.json" "the newest targets metadata"
newest=$(find "$work/tuf" -name '*.root.json' | sort -V | tail -n 1)
if [[ -n $newest ]]; then
  same "${files[1]}" "$newest" "the newest TUF root"
elif ! git diff --quiet "$base" -- "${files[1]}"; then
  echo "${files[1]} changed, and Sigstore published no TUF root after the one of $base" >&2
  exit 1
fi
echo "The compiled-in trust root is the update Sigstore publishes today, and the code of $base accepts it."
