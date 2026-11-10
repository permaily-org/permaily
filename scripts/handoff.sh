#!/usr/bin/env bash
# Asks the running enclave to hand the root secret to the release that will
# run next (ADR 0013). Run it before starting the new image.
#
#   scripts/handoff.sh <running> <next>
#
# <running> and <next> are release tags (v0.7.0), or directories that hold the
# pcrs.json of a staging image (the artifact of the staging-image workflow).
#
# The enclave decides alone whether to hand over: it checks, offline, that
# <next> was built by the release workflow and published in the public
# transparency log after <running>, and that its KMS key can only ever be used
# by that image. This script only fetches the public files it needs for that:
# each pcrs.json and the Sigstore bundle GitHub holds for it.
#
# Environment:
#   PERMAILY_API          base URL of the enclave (default https://api.permaily.com)
#   PERMAILY_ADMIN_TOKEN  admin token (default: ~/.config/permaily/admin-token)
#   PERMAILY_CA_FILE      extra CA certificate, for a staging image
#   PERMAILY_REPO         repository (default permaily-org/permaily)
#
# Needs gh, jq and curl.
set -euo pipefail

[[ $# -eq 2 ]] || { sed -n '2,22p' "$0"; exit 2; }
repo=${PERMAILY_REPO:-permaily-org/permaily}
api=${PERMAILY_API:-https://api.permaily.com}
token=${PERMAILY_ADMIN_TOKEN:-$(cat "${HOME}/.config/permaily/admin-token")}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# fetch NAME SOURCE: leaves $work/NAME/pcrs.json and $work/NAME/bundle.json.
fetch() {
  local name=$1 src=$2 dir=$work/$1
  mkdir -p "$dir/attestations"
  if [[ -d $src ]]; then
    cp "$src/pcrs.json" "$dir/pcrs.json"
  else
    gh release download "$src" --repo "$repo" --pattern pcrs.json --dir "$dir"
  fi
  (cd "$dir/attestations" && gh attestation download ../pcrs.json --repo "$repo" >/dev/null)
  # GitHub holds more than one attestation for a file. The enclave wants the
  # build provenance, the one recorded in the public transparency log.
  jq -c 'select((.dsseEnvelope.payload | @base64d | fromjson | .predicateType) == "https://slsa.dev/provenance/v1"
               and (.verificationMaterial.tlogEntries | length) == 1)' "$dir"/attestations/*.jsonl | head -n 1 > "$dir/bundle.json"
  [[ -s $dir/bundle.json ]] || { echo "no build provenance in the public log for $src" >&2; exit 1; }
  echo "$name: $src, PCR0 $(jq -r .PCR0 "$dir/pcrs.json")"
}

fetch from "$1"
fetch to "$2"

b64() { base64 -w0 "$1" > "$1.b64"; }
for f in from/pcrs.json from/bundle.json to/pcrs.json to/bundle.json; do b64 "$work/$f"; done
jq -n --rawfile to_pcrs "$work/to/pcrs.json.b64" --rawfile to_bundle "$work/to/bundle.json.b64" \
  --rawfile from_pcrs "$work/from/pcrs.json.b64" --rawfile from_bundle "$work/from/bundle.json.b64" \
  '{to_pcrs: $to_pcrs, to_bundle: $to_bundle, from_pcrs: $from_pcrs, from_bundle: $from_bundle}' > "$work/request.json"

status=$(curl -sS ${PERMAILY_CA_FILE:+--cacert "$PERMAILY_CA_FILE"} -o "$work/response.json" -w '%{http_code}' \
  -X POST -H "Content-Type: application/json" -H @<(printf 'Authorization: Bearer %s' "$token") \
  --data-binary @"$work/request.json" "$api/v1/admin/handoff")
jq . "$work/response.json" 2>/dev/null || cat "$work/response.json"
if [[ $status != 200 ]]; then
  echo "handoff refused or failed (HTTP $status)" >&2
  exit 1
fi
echo "The running enclave handed the root secret to $2. Start that image now."
