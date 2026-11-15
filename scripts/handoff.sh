#!/usr/bin/env bash
# Asks the running enclave to hand the root secret to the release that will
# run next (ADR 0013). Run it before starting the new image.
#
#   scripts/handoff.sh <running> <next>
#
# <running> and <next> are release tags (v0.7.0), or directories that hold the
# pcrs.json of a staging image (the artifact of the staging-image workflow).
#
# Before it sends anything, it verifies the enclave at PERMAILY_API with
# permaily-verify against the pcrs.json of <running> (for a tag, the release's,
# after checking its build provenance), and the token then goes only to the TLS
# key that verification attested (scripts/attested.sh). When the enclave does
# not verify as <running>, nothing is sent.
#
# The enclave decides alone whether to hand over: it checks, offline, that
# <next> was built by the release workflow and published in the public
# transparency log after <running>, and that its KMS key can only ever be used
# by that image. This script only fetches the public files it needs for that:
# each pcrs.json and the Sigstore bundle GitHub holds for it, and the trust
# root Sigstore publishes today (scripts/sigstore-trust-root.sh --send).
#
# Environment:
#   PERMAILY_API          base URL of the enclave (default https://api.permaily.com)
#   PERMAILY_ADMIN_TOKEN  admin token (default: ~/.config/permaily/admin-token)
#   PERMAILY_CA_FILE      extra CA certificate, for a staging image
#   PERMAILY_REPO         repository (default permaily-org/permaily)
#   PERMAILY_VERIFY       permaily-verify binary (default: built from this checkout)
#
# Needs gh, jq and curl, and go unless PERMAILY_VERIFY is set.
set -euo pipefail

[[ $# -eq 2 ]] || { sed -n '2,29p' "$0"; exit 2; }
repo=${PERMAILY_REPO:-permaily-org/permaily}
api=${PERMAILY_API:-https://api.permaily.com}
token=${PERMAILY_ADMIN_TOKEN:-$(cat "${HOME}/.config/permaily/admin-token")}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=scripts/attested.sh
source "$(dirname "$0")/attested.sh"

# measurements NAME SOURCE: leaves $work/NAME/pcrs.json. A release's is
# checked against its build provenance before anything relies on it.
measurements() {
  local src=$2 dir=$work/$1
  mkdir -p "$dir"
  if [[ -d $src ]]; then
    cp "$src/pcrs.json" "$dir/pcrs.json"
  else
    gh release download "$src" --repo "$repo" --pattern pcrs.json --dir "$dir"
    gh attestation verify "$dir/pcrs.json" --repo "$repo" \
      --signer-workflow "$repo/.github/workflows/release.yml" --source-ref refs/heads/main >/dev/null
  fi
}

# bundle NAME SOURCE: leaves $work/NAME/bundle.json, the Sigstore bundle the
# enclave is given with that pcrs.json.
bundle() {
  local dir=$work/$1
  PERMAILY_REPO=$repo "$(dirname "$0")/provenance-bundle.sh" "$dir/pcrs.json" "$dir/bundle.json"
  echo "$1: $2, PCR0 $(jq -r .PCR0 "$dir/pcrs.json")"
}

# The enclave that answers must be <running>, verified now; sigstore-trust-root.sh
# --send below verifies it again, against the same measurements.
measurements from "$1"
export PERMAILY_PCRS=$work/from/pcrs.json
attest
if [[ -z ${PERMAILY_VERIFY:-} && -x $work/permaily-verify ]]; then export PERMAILY_VERIFY=$work/permaily-verify; fi
bundle from "$1"
measurements to "$2"
bundle to "$2"

# The next release may be signed with something Sigstore added after the
# running image was built. Give the enclave the trust root Sigstore publishes
# today: it takes it only with the signatures of Sigstore's keyholders, and it
# is a no-op when it has it already.
PERMAILY_API=$api PERMAILY_ADMIN_TOKEN=$token "$(dirname "$0")/sigstore-trust-root.sh" --send

b64() { base64 -w0 "$1" > "$1.b64"; }
for f in from/pcrs.json from/bundle.json to/pcrs.json to/bundle.json; do b64 "$work/$f"; done
jq -n --rawfile to_pcrs "$work/to/pcrs.json.b64" --rawfile to_bundle "$work/to/bundle.json.b64" \
  --rawfile from_pcrs "$work/from/pcrs.json.b64" --rawfile from_bundle "$work/from/bundle.json.b64" \
  '{to_pcrs: $to_pcrs, to_bundle: $to_bundle, from_pcrs: $from_pcrs, from_bundle: $from_bundle}' > "$work/request.json"

status=$(enclave_curl -o "$work/response.json" -w '%{http_code}' \
  -X POST -H "Content-Type: application/json" -H @<(printf 'Authorization: Bearer %s' "$token") \
  --data-binary @"$work/request.json" "$api/v1/admin/handoff") || exit 1
if [[ $status != 200 ]]; then
  enclave_error "$work/response.json"
  echo "handoff refused or failed (HTTP $status)" >&2
  exit 1
fi
# What the enclave answers, field by field: nothing else of the answer is printed.
jq -r 'def clean: tostring | gsub("[[:cntrl:]]"; " ");
  "Handed to image \(.image | clean), commit \(.commit | clean), published \(.published_at | clean),",
  "under KMS key \(.key_arn | clean)\(if .created == false then " (that release already had its record)" else "" end)."' "$work/response.json"
echo "The running enclave handed the root secret to $2. Start that image now."
