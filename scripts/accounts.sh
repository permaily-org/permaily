#!/usr/bin/env bash
# Shows the accounts as the enclave vouches for them: read from their signed
# records, not from the table. It only looks: nothing here changes an account,
# and a person deletes their own.
#
#   scripts/accounts.sh             every account, the oldest first
#   scripts/accounts.sh acc_...     one account
#
# Environment:
#   PERMAILY_API          base URL of the enclave (default https://api.permaily.com)
#   PERMAILY_ADMIN_TOKEN  admin token (default: ~/.config/permaily/admin-token)
#   PERMAILY_CA_FILE      extra CA certificate, for a staging image
#
# Needs curl and jq.
set -euo pipefail

id=${1:-}
[[ $# -le 1 && ( -z $id || $id =~ ^acc_[a-z2-7]{26}$ ) ]] || { sed -n '2,14p' "$0"; exit 2; }
api=${PERMAILY_API:-https://api.permaily.com}
token=${PERMAILY_ADMIN_TOKEN:-$(cat "${HOME}/.config/permaily/admin-token")}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# get <path and query> writes the answer to $work/response.json.
get() {
  local code
  code=$(curl -sS ${PERMAILY_CA_FILE:+--cacert "$PERMAILY_CA_FILE"} -o "$work/response.json" -w '%{http_code}' \
    -H @<(printf 'Authorization: Bearer %s' "$token") "$api$1")
  if [[ $code != 200 ]]; then
    jq -r '.error.message // .' "$work/response.json" >&2 2>/dev/null || cat "$work/response.json" >&2
    echo "the enclave at $api did not answer $1 (HTTP $code)" >&2
    exit 1
  fi
}

# What is shown of a time: to the minute, in UTC.
when='def when: if . == null then "-" else .[0:16] | sub("T"; " ") end;'

if [[ -n $id ]]; then
  get "/v1/admin/accounts/$id"
  jq -r "$when"'
    ["Account", .id],
    ["Address", .address],
    ["Name", (if .name == "" then "-" else .name end)],
    ["State", (if .state == "closing" then "closing, removed on \(.closes_at | when) UTC" else .state end)],
    ["Language", .language],
    ["Created", "\(.created_at | when) UTC"],
    ["Address verified", "\(.address_verified_at | when) UTC"],
    ["Passkeys", "\(.passkeys)\(if .passkey_only then ", and nothing else signs in (passkey only)" else "" end)"],
    ["Sessions", "\(.sessions)"]
    | "\(.[0] + ":" | . + " " * (18 - length))\(.[1])"' "$work/response.json"
  exit 0
fi

# Every page, one account a line.
: > "$work/accounts.jsonl"
after="" unreadable=0
while :; do
  get "/v1/admin/accounts?limit=100${after:+&after=$(jq -rn --arg v "$after" '$v | @uri')}"
  jq -c '.accounts[]' "$work/response.json" >> "$work/accounts.jsonl"
  unreadable=$((unreadable + $(jq -r .unreadable "$work/response.json")))
  after=$(jq -r '.next // ""' "$work/response.json")
  [[ -n $after ]] || break
done

# Columns as wide as what they hold; a name is cut at 30 characters.
jq -rs "$when"'
  def cell($w): tostring | if length > $w then .[0:$w - 1] + "…" else . + " " * ($w - length) end;
  map([.id, .address, (if .name == "" then "-" else .name end),
       (if .state == "closing" then "closing \(.closes_at | when)" else .state end),
       (.created_at | when), "\(.passkeys)\(if .passkey_only then " only" else "" end)", "\(.sessions)"])
  | [["ACCOUNT", "ADDRESS", "NAME", "STATE", "CREATED (UTC)", "PASSKEYS", "SESSIONS"]] + .
  | . as $rows
  | [range(0; 7) as $i | [$rows[][$i] | length] | max | if $i == 2 and . > 30 then 30 else . end] as $widths
  | $rows[] | [range(0; 7) as $i | .[$i] | cell($widths[$i])] | join("  ") | sub(" +$"; "")' "$work/accounts.jsonl"
count=$(wc -l < "$work/accounts.jsonl" | tr -d ' ')
echo "$count account(s) at $api, as the enclave's signed records say." >&2
if [[ $unreadable != 0 ]]; then
  echo "$unreadable more could not be read: their records are missing or do not verify. Nothing is shown of them." >&2
  exit 1
fi
