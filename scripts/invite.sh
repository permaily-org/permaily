#!/usr/bin/env bash
# Makes single-use invitations to sign up, lists the ones not used yet and
# revokes one. An invitation is shown once, when it is made: the enclave
# keeps only its hash. Each works for thirty days, for one new account.
#
#   scripts/invite.sh                 one invitation
#   scripts/invite.sh 5               five (at most 100 at a time)
#   scripts/invite.sh list            the ones not used yet: identifier, made, ends
#   scripts/invite.sh revoke inv_...  makes one of no use; --yes after it skips the question
#
# The invitations go to standard output, one a line. Their identifiers, in
# the same order, go to standard error: an identifier is what an invitation
# is listed and revoked by, it is not the code, and the code cannot be had
# from it. An invitation that was used is deleted when it is used, so the
# list does not show it; nor the ones made by a release without the list.
#
# Environment:
#   PERMAILY_API          base URL of the enclave (default https://api.permaily.com)
#   PERMAILY_ADMIN_TOKEN  admin token (default: ~/.config/permaily/admin-token)
#   PERMAILY_CA_FILE      extra CA certificate, for a staging image
#
# Needs curl and jq.
set -euo pipefail

usage() { sed -n '2,22p' "$0"; exit 2; }

action=make count=1 id="" yes=""
case ${1:-} in
  "") ;;
  list) action=list; shift ;;
  revoke) action=revoke id=${2:-}; [[ $id =~ ^inv_[a-z2-7]{26}$ ]] || usage; shift 2 ;;
  *) count=$1; [[ $count =~ ^[0-9]+$ ]] || usage; shift ;;
esac
if [[ $action == revoke && ${1:-} == --yes ]]; then yes=1; shift; fi
[[ $# -eq 0 ]] || usage

api=${PERMAILY_API:-https://api.permaily.com}
token=${PERMAILY_ADMIN_TOKEN:-$(cat "${HOME}/.config/permaily/admin-token")}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# ask <method> <path> [body] writes the answer to $work/response.json and
# fails unless the enclave answered as wanted (200, or 204 for a DELETE).
ask() {
  local code want=200
  [[ $1 != DELETE ]] || want=204
  code=$(curl -sS ${PERMAILY_CA_FILE:+--cacert "$PERMAILY_CA_FILE"} -o "$work/response.json" -w '%{http_code}' \
    -X "$1" -H "Content-Type: application/json" -H @<(printf 'Authorization: Bearer %s' "$token") \
    ${3:+--data-binary "$3"} "$api$2")
  if [[ $code != "$want" ]]; then
    jq -r '.error.message // .' "$work/response.json" >&2 2>/dev/null || cat "$work/response.json" >&2
    echo "the enclave at $api did not do it: $1 $2 (HTTP $code)" >&2
    exit 1
  fi
}

# What is shown of a time: to the minute, in UTC.
when='def when: .[0:16] | sub("T"; " ");'

case $action in
  make)
    ask POST /v1/admin/invitations "{\"count\": $count}"
    jq -r '.invitations[]' "$work/response.json"
    echo "Valid until $(jq -r .expires_at "$work/response.json"), each for one new account." >&2
    if jq -e 'has("ids")' "$work/response.json" > /dev/null; then
      echo "Their identifiers, in the same order, to list or revoke them by:" >&2
      jq -r '.ids[]' "$work/response.json" >&2
    else
      echo "This release gives no identifiers, so these cannot be listed or revoked." >&2
    fi
    ;;

  list)
    ask GET /v1/admin/invitations
    jq -r "$when"'
      .invitations
      | map([.id, (.created_at | when), (.expires_at | when)])
      | [["INVITATION", "MADE (UTC)", "ENDS (UTC)"]] + .
      | .[] | "\(.[0] + " " * (30 - (.[0] | length)))  \(.[1] + " " * (16 - (.[1] | length)))  \(.[2])"' "$work/response.json"
    echo "$(jq -r '.invitations | length' "$work/response.json") invitation(s) not used yet at $api. One that was used is deleted when it is used, so it is not here." >&2
    if [[ $(jq -r '.truncated // false' "$work/response.json") == true ]]; then
      echo "There are more than the list holds." >&2
    fi
    ;;

  revoke)
    ask GET /v1/admin/invitations
    if ! jq -e --arg id "$id" '.invitations | any(.id == $id)' "$work/response.json" > /dev/null; then
      echo "No invitation $id is waiting to be used: it was used, revoked, ended, or never made." >&2
      exit 1
    fi
    echo "This will revoke the invitation $id: nobody can sign up with it from then on, not even a sign-up that was started with it." >&2
    jq -r --arg id "$id" "$when"'.invitations[] | select(.id == $id) | "Made \(.created_at | when) UTC, ends \(.expires_at | when) UTC."' "$work/response.json" >&2
    echo "It is counted on /status." >&2
    if [[ -z $yes ]]; then
      read -r -p "Type yes to go on: " answer
      if [[ $answer != yes ]]; then
        echo "Nothing was done." >&2
        exit 1
      fi
    fi
    ask DELETE "/v1/admin/invitations/$id"
    echo "Revoked." >&2
    ;;
esac
