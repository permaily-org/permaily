#!/usr/bin/env bash
# Makes single-use invitations to sign up, and prints them. They are shown
# once: the enclave keeps only their hash. Each works for thirty days, for one
# new account.
#
#   scripts/invite.sh          one invitation
#   scripts/invite.sh 5        five (at most 100 at a time)
#
# Environment:
#   PERMAILY_API          base URL of the enclave (default https://api.permaily.com)
#   PERMAILY_ADMIN_TOKEN  admin token (default: ~/.config/permaily/admin-token)
#   PERMAILY_CA_FILE      extra CA certificate, for a staging image
#
# Needs curl and jq.
set -euo pipefail

count=${1:-1}
[[ $count =~ ^[0-9]+$ ]] || { sed -n '2,14p' "$0"; exit 2; }
api=${PERMAILY_API:-https://api.permaily.com}
token=${PERMAILY_ADMIN_TOKEN:-$(cat "${HOME}/.config/permaily/admin-token")}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

code=$(curl -sS ${PERMAILY_CA_FILE:+--cacert "$PERMAILY_CA_FILE"} -o "$work/response.json" -w '%{http_code}' \
  -X POST -H "Content-Type: application/json" -H @<(printf 'Authorization: Bearer %s' "$token") \
  --data-binary "{\"count\": $count}" "$api/v1/admin/invitations")
if [[ $code != 200 ]]; then
  jq -r '.error.message // .' "$work/response.json" 2>/dev/null >&2 || cat "$work/response.json" >&2
  echo "the enclave at $api made no invitations (HTTP $code)" >&2
  exit 1
fi
jq -r '.invitations[]' "$work/response.json"
echo "Valid until $(jq -r .expires_at "$work/response.json"), each for one new account." >&2
