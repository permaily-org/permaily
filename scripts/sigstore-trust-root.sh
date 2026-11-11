#!/usr/bin/env bash
# Compares the Sigstore trust root compiled into the enclave and the verifier
# (pkg/provenance/sigstore_trusted_root.json) with the one Sigstore publishes
# today. gh fetches it through Sigstore's TUF repository, which checks the
# signatures of Sigstore's root keyholders.
#
#   scripts/sigstore-trust-root.sh            compare; exit 1 when they differ
#   scripts/sigstore-trust-root.sh --update   replace the compiled-in file
#
# A difference is not an attack on its own: Sigstore adds and retires keys. It
# means a release with the new file must be deployed before the old keys stop
# signing, or the running enclave will accept no release at all
# (docs/operations.md, "Sigstore changed"). Needs gh and jq.
set -euo pipefail

cd "$(dirname "$0")/.."
file=pkg/provenance/sigstore_trusted_root.json
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

gh attestation trusted-root > "$work/roots.jsonl"
# gh prints one trust root per line; Sigstore's public one names its Rekor log.
jq -c 'select(any(.tlogs[]?; .baseUrl == "https://rekor.sigstore.dev"))' "$work/roots.jsonl" > "$work/published.json"
[[ $(wc -l < "$work/published.json") -eq 1 ]] || { echo "gh did not return Sigstore's public trust root" >&2; exit 2; }

if [[ ${1:-} == --update ]]; then
  jq . "$work/published.json" > "$file"
  echo "Updated $file. Run the tests, then release and deploy it before the old keys stop signing."
  exit 0
fi

if diff <(jq -S . "$file") <(jq -S . "$work/published.json") > "$work/diff"; then
  echo "The compiled-in Sigstore trust root is the one Sigstore publishes today."
  exit 0
fi
echo "Sigstore's trust root changed ('<' compiled in, '>' published today):"
cat "$work/diff"
echo
echo "::error::Sigstore's trust root is not the one compiled into the enclave. See \"Sigstore changed\" in docs/operations.md."
exit 1
