#!/usr/bin/env bash
# Verifies production with the verifier and the measurements published in the
# latest release, the way the scheduled `verify-production` workflow does.
#
#   scripts/verify-production.sh [extra permaily-verify flags]
#
# Between the publication of a release and its deployment production still runs
# the previous one, so when the latest release does not verify the previous one
# is tried: if that one passes, the result is a pass with a notice.
#
# This trusts the release files (checked against their Sigstore provenance)
# instead of a local rebuild; scripts/verify.sh is the full check. Needs gh and jq.
set -euo pipefail

repo=${PERMAILY_REPO:-permaily-org/permaily}
case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 2 ;;
esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# check TAG [flags]: runs that release's verifier against production.
check() {
  local tag=$1; shift
  local dir=$work/$tag verifier=permaily-verify-linux-$arch
  mkdir -p "$dir"
  gh release download "$tag" --repo "$repo" --dir "$dir" --pattern pcrs.json --pattern "$verifier"
  for f in pcrs.json "$verifier"; do
    gh attestation verify "$dir/$f" --repo "$repo" \
      --signer-workflow "$repo/.github/workflows/release.yml" --source-ref refs/heads/main >/dev/null
  done
  chmod +x "$dir/$verifier"
  echo "== permaily-verify $tag =="
  "$dir/$verifier" -pcrs "$dir/pcrs.json" "$@"
}

mapfile -t tags < <(gh release list --repo "$repo" --limit 2 --exclude-drafts --exclude-pre-releases --json tagName -q '.[].tagName')
[[ ${#tags[@]} -ge 1 ]] || { echo "no releases in $repo" >&2; exit 2; }

if check "${tags[0]}" "$@"; then
  echo
  echo "RESULT: production runs ${tags[0]}, the latest release, and passes every check."
  exit 0
fi
if [[ ${#tags[@]} -ge 2 ]]; then
  echo
  echo "The latest release does not verify. Trying the previous one, in case it is not deployed yet."
  if check "${tags[1]}" "$@"; then
    echo
    echo "RESULT: production runs ${tags[1]} and passes every check; the latest release ${tags[0]} is not deployed."
    echo "::notice::production runs ${tags[1]}; ${tags[0]} is not deployed yet"
    exit 0
  fi
fi
echo
echo "RESULT: production does not verify against ${tags[*]}."
exit 1
