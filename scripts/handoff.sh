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
# after authenticating its build provenance), and the token then goes only to
# the TLS key that verification attested (scripts/attested.sh). When the
# enclave does not verify as <running>, nothing is sent. The verifier is
# <running>'s own: for a tag, built from the commit that provenance names (never
# from the tag's name alone), fetched into a private repository and built in a
# fixed Go environment (scripts/attested.sh), and the same binary verifies
# again, against the same authenticated pcrs.json, and decides about the trust
# root in sigstore-trust-root.sh --send.
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
#   PERMAILY_VERIFY       permaily-verify binary (default: built from the commit
#                         <running>'s build provenance names, or for a directory
#                         from this checkout, which must then be the staging
#                         image's source and is trusted as such)
#
# Needs gh, jq, curl and git (2.32 or later), and go unless PERMAILY_VERIFY is set.
set -euo pipefail

[[ $# -eq 2 ]] || { sed -n '2,37p' "$0"; exit 2; }
api=${PERMAILY_API:-https://api.permaily.com}
# shellcheck source=scripts/attested.sh
source "$(dirname "$0")/attested.sh"
work=$(mktemp -d)
trap attested_cleanup EXIT
load_token

# measurements NAME SOURCE: leaves $work/NAME/pcrs.json. A release's is
# authenticated by its build provenance before anything relies on it, the one
# way every script does (release_pcrs, scripts/attested.sh): for <running>, that
# also names the commit its verifier is built from.
measurements() {
  local src=$2 dir=$work/$1
  mkdir -p "$dir"
  if [[ -d $src ]]; then
    cp "$src/pcrs.json" "$dir/pcrs.json"
  else
    release_pcrs "$src" "$dir"
  fi
}

# bundle NAME SOURCE: leaves $work/NAME/bundle.json, the Sigstore bundle the
# enclave is given with that pcrs.json.
bundle() {
  local dir=$work/$1
  PERMAILY_REPO=$release_repository "$(dirname "$0")/provenance-bundle.sh" "$dir/pcrs.json" "$dir/bundle.json"
  echo "$1: $2, PCR0 $(jq -r .PCR0 "$dir/pcrs.json")"
}

# The enclave that answers must be <running>, verified now by <running>'s own
# verifier (for a tag, built from its source); sigstore-trust-root.sh --send
# below verifies it again, with the same verifier and the same measurements.
measurements from "$1"
export PERMAILY_PCRS=$work/from/pcrs.json
if [[ -d $1 ]]; then unset PERMAILY_RELEASE; else export PERMAILY_RELEASE=$1; fi
attest
if [[ -n ${verifier:-} ]]; then export PERMAILY_VERIFY=$verifier; fi
export PERMAILY_API=$api
bundle from "$1"
measurements to "$2"
bundle to "$2"

# The next release may be signed with something Sigstore added after the
# running image was built. Give the enclave the trust root Sigstore publishes
# today: it takes it only with the signatures of Sigstore's keyholders, and it
# is a no-op when it has it already.
with_token "$(dirname "$0")/sigstore-trust-root.sh" --send

# base64 without line breaks, with GNU's or macOS's.
b64() { base64 < "$1" | tr -d '\n' > "$1.b64"; }
for f in from/pcrs.json from/bundle.json to/pcrs.json to/bundle.json; do b64 "$work/$f"; done
jq -n --rawfile to_pcrs "$work/to/pcrs.json.b64" --rawfile to_bundle "$work/to/bundle.json.b64" \
  --rawfile from_pcrs "$work/from/pcrs.json.b64" --rawfile from_bundle "$work/from/bundle.json.b64" \
  '{to_pcrs: $to_pcrs, to_bundle: $to_bundle, from_pcrs: $from_pcrs, from_bundle: $from_bundle}' > "$work/request.json"

status=$(enclave_curl --token -o "$work/response.json" -w '%{http_code}' \
  -X POST -H "Content-Type: application/json" \
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
