#!/usr/bin/env bash
# Shows the accounts as the enclave vouches for them (read from their signed
# records, not from the table), and does to one what an operator may do.
#
#   scripts/accounts.sh                           every account, the oldest first
#   scripts/accounts.sh acc_...                   one account
#   scripts/accounts.sh find <address>            the account of exactly that address
#   scripts/accounts.sh stats                     how many accounts, by state
#   scripts/accounts.sh block <account>           nobody signs in to it; its devices are signed out
#   scripts/accounts.sh unblock <account>         its person can sign in again
#   scripts/accounts.sh delete <account>          removed in seven days, unless that is cancelled
#   scripts/accounts.sh cancel <account>          cancels a deletion, whoever asked for it
#   scripts/accounts.sh close-sessions <account>  signs every device out
#
# <account> is an identifier (acc_...) or the account's address. A command
# that changes something says what it will do, and to which account, and asks
# for a "yes"; --yes after it skips the question.
#
# There is no command to change an address or a way to sign in, to turn
# "passkey only" off or to sign in as someone: the enclave has no such route.
# What is done here is counted, by kind, on /status for anyone to see.
#
# Environment:
#   PERMAILY_API          base URL of the enclave (default https://api.permaily.com)
#   PERMAILY_ADMIN_TOKEN  admin token (default: ~/.config/permaily/admin-token)
#   PERMAILY_CA_FILE      extra CA certificate, for a staging image
#
# Needs curl and jq.
set -euo pipefail

usage() { sed -n '2,28p' "$0"; exit 2; }

action=list target="" yes=""
case ${1:-} in
  "") ;;
  acc_*) action=show target=$1; shift ;;
  find|block|unblock|delete|cancel|close-sessions) action=$1 target=${2:-}; [[ -n $target ]] || usage; shift 2 ;;
  stats) action=stats; shift ;;
  *) usage ;;
esac
if [[ ${1:-} == --yes ]]; then yes=1; shift; fi
[[ $# -eq 0 ]] || usage
[[ -z $yes || $action =~ ^(block|unblock|delete|cancel|close-sessions)$ ]] || usage

api=${PERMAILY_API:-https://api.permaily.com}
token=${PERMAILY_ADMIN_TOKEN:-$(cat "${HOME}/.config/permaily/admin-token")}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# ask <method> <path and query> writes the answer to $work/response.json.
ask() {
  local code
  code=$(curl -sS ${PERMAILY_CA_FILE:+--cacert "$PERMAILY_CA_FILE"} -o "$work/response.json" -w '%{http_code}' \
    -X "$1" -H @<(printf 'Authorization: Bearer %s' "$token") "$api$2")
  if [[ $code != 200 ]]; then
    jq -r '.error.message // .' "$work/response.json" >&2 2>/dev/null || cat "$work/response.json" >&2
    echo "the enclave at $api did not answer $2 (HTTP $code)" >&2
    exit 1
  fi
}

# What is shown of a time: to the minute, in UTC.
when='def when: if . == null then "-" else .[0:16] | sub("T"; " ") end;'

# show prints one account, from a file that holds its record.
show() {
  jq -r "$when"'
    ["Account", .id],
    ["Address", .address],
    ["Name", (if .name == "" then "-" else .name end)],
    ["State", (if .state == "closing" then "closing, removed on \(.closes_at | when) UTC, asked by the \(.closing_by // "person")" else .state end)],
    ["Blocked", (if .blocked_at then "since \(.blocked_at | when) UTC: nobody signs in to it" else "no" end)],
    ["Language", .language],
    ["Created", "\(.created_at | when) UTC"],
    ["Address verified", "\(.address_verified_at | when) UTC"],
    ["Passkeys", "\(.passkeys)\(if .passkey_only then ", and nothing else signs in (passkey only)" else "" end)"],
    ["Providers", (if (.providers // []) == [] then "-" else .providers | join(", ") end)],
    ["Sessions", "\(.sessions)"]
    | "\(.[0] + ":" | . + " " * (18 - length))\(.[1])"' "$1"
}

# by_address writes the record of the account of an address to
# $work/account.json, and fails when the address has none.
by_address() {
  ask GET "/v1/admin/accounts?address=$(jq -rn --arg v "$1" '$v | @uri')"
  if [[ $(jq -r .unreadable "$work/response.json") != 0 ]]; then
    echo "$1 has an account whose record is missing or does not verify. Nothing is shown of it, and nothing is done to it." >&2
    exit 1
  fi
  if [[ $(jq -r '.accounts | length' "$work/response.json") != 1 ]]; then
    echo "No account has the address $1." >&2
    exit 1
  fi
  jq '.accounts[0]' "$work/response.json" > "$work/account.json"
}

# resolve writes the record of an account, named by its identifier or by its
# address, to $work/account.json.
resolve() {
  if [[ $1 =~ ^acc_[a-z2-7]{26}$ ]]; then
    ask GET "/v1/admin/accounts/$1"
    cp "$work/response.json" "$work/account.json"
  elif [[ $1 == *@* ]]; then
    by_address "$1"
  else
    usage
  fi
}

# pages writes every account, one a line, to $work/accounts.jsonl, and sets
# unreadable to how many could not be read.
pages() {
  : > "$work/accounts.jsonl"
  local after=""
  unreadable=0
  while :; do
    ask GET "/v1/admin/accounts?limit=100${after:+&after=$(jq -rn --arg v "$after" '$v | @uri')}"
    jq -c '.accounts[]' "$work/response.json" >> "$work/accounts.jsonl"
    unreadable=$((unreadable + $(jq -r .unreadable "$work/response.json")))
    after=$(jq -r '.next // ""' "$work/response.json")
    [[ -n $after ]] || break
  done
}

# unread says how many accounts could not be read, and fails if any.
unread() {
  if [[ $unreadable != 0 ]]; then
    echo "$unreadable more could not be read: their records are missing or do not verify. Nothing is shown of them." >&2
    exit 1
  fi
}

case $action in
  show)
    resolve "$target"
    show "$work/account.json"
    ;;

  find)
    [[ $target == *@* ]] || usage
    by_address "$target"
    show "$work/account.json"
    ;;

  block|unblock|delete|cancel|close-sessions)
    resolve "$target"
    id=$(jq -r .id "$work/account.json")
    case $action in
      block) path=block what="block the account: every device is signed out, and nobody signs in to it until it is unblocked" ;;
      unblock) path=unblock what="unblock the account: its person can sign in again" ;;
      delete) path=delete what="ask to delete the account: it is removed in seven days, with everything in it, unless that is cancelled" ;;
      cancel) path=delete/cancel what="cancel the deletion of the account, whoever asked for it" ;;
      close-sessions) path=sessions/close what="sign every device out of the account" ;;
    esac
    echo "This will $what." >&2
    echo >&2
    show "$work/account.json" >&2
    echo >&2
    echo "Its person is told by mail of a block, an unblock, a deletion asked and one cancelled. It is counted on /status." >&2
    if [[ -z $yes ]]; then
      read -r -p "Type yes to go on: " answer
      if [[ $answer != yes ]]; then
        echo "Nothing was done." >&2
        exit 1
      fi
    fi
    ask POST "/v1/admin/accounts/$id/$path"
    echo "Done. The account now:" >&2
    echo >&2
    show "$work/response.json"
    ;;

  stats)
    pages
    jq -rs '
      def n(f): map(select(f)) | length;
      ["Accounts", length],
      ["Active", n(.state == "active")],
      ["Closing", n(.state == "closing")],
      ["  asked by the person", n(.state == "closing" and (.closing_by // "person") == "person")],
      ["  asked by the operator", n(.state == "closing" and .closing_by == "operator")],
      ["Blocked", n(.blocked_at != null)],
      ["  and closing", n(.blocked_at != null and .state == "closing")],
      ["Passkey only", n(.passkey_only)],
      ["With a passkey", n(.passkeys > 0)],
      ["With a provider", n((.providers // []) != [])],
      ["Signed in somewhere", n(.sessions > 0)]
      | "\(.[0] + ":" | . + " " * (26 - length))\(.[1])"' "$work/accounts.jsonl"
    echo "Counted at $api from the enclave's signed records, a page at a time. Blocked is apart from the state: a blocked account is also active or closing." >&2
    unread
    ;;

  list)
    pages
    # Columns as wide as what they hold; a name is cut at 30 characters.
    jq -rs "$when"'
      def cell($w): tostring | if length > $w then .[0:$w - 1] + "…" else . + " " * ($w - length) end;
      map([.id, .address, (if .name == "" then "-" else .name end),
           ((if .state == "closing" then "closing \(.closes_at | when)" else .state end) + (if .blocked_at then ", blocked" else "" end)),
           (.created_at | when), "\(.passkeys)\(if .passkey_only then " only" else "" end)", "\(.sessions)"])
      | [["ACCOUNT", "ADDRESS", "NAME", "STATE", "CREATED (UTC)", "PASSKEYS", "SESSIONS"]] + .
      | . as $rows
      | [range(0; 7) as $i | [$rows[][$i] | length] | max | if $i == 2 and . > 30 then 30 else . end] as $widths
      | $rows[] | [range(0; 7) as $i | .[$i] | cell($widths[$i])] | join("  ") | sub(" +$"; "")' "$work/accounts.jsonl"
    count=$(wc -l < "$work/accounts.jsonl" | tr -d ' ')
    echo "$count account(s) at $api, as the enclave's signed records say." >&2
    unread
    ;;
esac
