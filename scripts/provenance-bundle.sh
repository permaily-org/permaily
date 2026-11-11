#!/usr/bin/env bash
# Writes the Sigstore bundle that makes a pcrs.json a release: the build
# provenance GitHub holds for that file, recorded in the public transparency
# log. It is what a running enclave is given, with the file, to decide whether
# to hand the root secret over (ADR 0013).
#
#   scripts/provenance-bundle.sh <pcrs.json> <bundle.json>
#
# Environment: PERMAILY_REPO, the repository (default permaily-org/permaily).
# Needs gh and jq.
set -euo pipefail

[[ $# -eq 2 ]] || { sed -n '2,11p' "$0"; exit 2; }
repo=${PERMAILY_REPO:-permaily-org/permaily}
pcrs=$(realpath "$1")
out=$(realpath "$2")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# An attestation made a moment ago can take a few seconds to be served.
for attempt in 1 2 3 4 5 6; do
  if (cd "$work" && gh attestation download "$pcrs" --repo "$repo" >/dev/null 2>"$work/error"); then
    break
  fi
  [[ $attempt -lt 6 ]] || { cat "$work/error" >&2; exit 1; }
  sleep 5
done

# GitHub holds more than one attestation for a file. The enclave wants the
# build provenance, the one recorded in the public transparency log.
jq -c 'select((.dsseEnvelope.payload | @base64d | fromjson | .predicateType) == "https://slsa.dev/provenance/v1"
             and (.verificationMaterial.tlogEntries | length) == 1)' "$work"/*.jsonl | head -n 1 > "$out"
[[ -s $out ]] || { echo "no build provenance in the public log for $1" >&2; exit 1; }
