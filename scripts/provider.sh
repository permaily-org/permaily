#!/usr/bin/env bash
# Gives the enclave the application registered at Google, GitHub, Meta or
# Microsoft, so people can sign in with it; takes one away; or lists which
# are set. The secret goes to the enclave over TLS and is kept there sealed:
# it is never answered back, and this script never prints it. Run
# scripts/verify.sh first: the secret is sent to whoever answers.
#
#   scripts/provider.sh                                   which are set, with their client ids
#   scripts/provider.sh set google <client-id>            the secret is read from standard input
#   scripts/provider.sh set google <client-id> <file>     the secret is read from a file
#   scripts/provider.sh remove google                     the provider is off from then
#
# The secret is never an argument: arguments are seen by every process of the
# machine, and stay in the shell's history. From a terminal it is asked for
# without being shown; otherwise pipe it in (pass show permaily/google | scripts/provider.sh set google <client-id>).
#
# Environment:
#   PERMAILY_API          base URL of the enclave (default https://api.permaily.com)
#   PERMAILY_ADMIN_TOKEN  admin token (default: ~/.config/permaily/admin-token)
#   PERMAILY_CA_FILE      extra CA certificate, for a staging image
#
# Needs curl and jq.
set -euo pipefail

usage() { sed -n '2,23p' "$0"; exit 2; }

action=${1:-list}
provider=${2:-}
case $action in
  list) [[ $# -le 1 ]] || usage ;;
  set) [[ $# -eq 3 || $# -eq 4 ]] || usage ;;
  remove) [[ $# -eq 2 ]] || usage ;;
  *) usage ;;
esac
if [[ $action != list && ! $provider =~ ^(google|github|meta|microsoft)$ ]]; then
  echo "$provider is not a provider: google, github, meta or microsoft" >&2
  exit 2
fi

api=${PERMAILY_API:-https://api.permaily.com}
if [[ $action == set && $api != https://* ]]; then
  echo "a secret is sent only over TLS: PERMAILY_API must start with https://" >&2
  exit 2
fi
token=${PERMAILY_ADMIN_TOKEN:-$(cat "${HOME}/.config/permaily/admin-token")}
# What is written here holds the secret for a moment: nobody else reads it.
umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# ask <method> <path> [file with the body] writes the answer to
# $work/response.json and its status to $code.
ask() {
  code=$(curl -sS ${PERMAILY_CA_FILE:+--cacert "$PERMAILY_CA_FILE"} -o "$work/response.json" -w '%{http_code}' \
    -X "$1" -H @<(printf 'Authorization: Bearer %s' "$token") \
    ${3:+-H "Content-Type: application/json" --data-binary "@$3"} "$api$2")
}

# refused prints why the enclave said no. Its answers never hold a secret.
refused() {
  jq -r '.error.message // .' "$work/response.json" >&2 2>/dev/null || cat "$work/response.json" >&2
  echo "the enclave at $api $1 (HTTP $code)" >&2
  exit 1
}

case $action in
  list)
    ask GET /v1/admin/providers
    [[ $code == 200 ]] || refused "did not list the providers"
    jq -r '
      (.providers | map({key: .provider, value: .}) | from_entries) as $set
      | ["google", "github", "meta", "microsoft"][]
      | . as $name
      | if $set[$name] then "\($name + " " * (10 - ($name | length)))on   \($set[$name].client_id)  (set \($set[$name].set_at[0:16] | sub("T"; " ")) UTC)"
        else "\($name + " " * (10 - ($name | length)))off" end' "$work/response.json"
    ;;
  set)
    client_id=$3
    secret_file=${4:-}
    if [[ -n $secret_file ]]; then
      [[ -r $secret_file ]] || { echo "cannot read $secret_file" >&2; exit 2; }
      cat -- "$secret_file" > "$work/secret"
    elif [[ -t 0 ]]; then
      # read is a builtin, so the secret is in no process's arguments.
      IFS= read -rs -p "Client secret of the $provider application: " secret
      echo >&2
      printf '%s' "$secret" > "$work/secret"
      unset secret
    else
      cat > "$work/secret"
    fi
    # The file may end with a line break, which is no part of the secret.
    jq -n --arg id "$client_id" --rawfile secret "$work/secret" \
      '{client_id: $id, client_secret: ($secret | sub("[\r\n]+$"; ""))}' > "$work/body.json"
    rm -f "$work/secret"
    ask PUT "/v1/admin/providers/$provider" "$work/body.json"
    rm -f "$work/body.json"
    [[ $code == 200 ]] || refused "did not take the application of $provider"
    echo "$provider is on at $api, with client id $(jq -r .client_id "$work/response.json")." >&2
    ;;
  remove)
    ask DELETE "/v1/admin/providers/$provider"
    [[ $code == 204 ]] || refused "did not remove the application of $provider"
    echo "$provider is off at $api. The accounts that linked it keep it, for when it is on again." >&2
    ;;
esac
