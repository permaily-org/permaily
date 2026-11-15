#!/usr/bin/env bash
# Gives the enclave the model's API key (the OpenAI project permaily-enclave,
# ADR 0028), replaces it, takes it away, or says whether one is set. The key
# goes to the enclave over TLS and is kept there sealed: it is never answered
# back, and this script never prints it. Before it sends anything, it verifies
# the enclave with permaily-verify, and the token and the key then go only to
# the TLS key that verification attested (scripts/attested.sh): never to
# another server, even one with a valid certificate for the name. When the
# enclave does not verify, nothing is sent.
#
#   scripts/model-key.sh                   whether a key is set, since when, and its fingerprint
#   scripts/model-key.sh set               the key is read from standard input
#   scripts/model-key.sh set <file>        the key is read from a file only you can read (mode 600 or 400)
#   scripts/model-key.sh remove            the model is off from then
#
# The key is never an argument: arguments are seen by every process of the
# machine, and stay in the shell's history. From a terminal it is asked for
# without being shown; otherwise pipe it in (pass show permaily/openai | scripts/model-key.sh set).
# The fingerprint is the first 16 hexadecimal digits of the key's SHA-256: compare it with your own copy's
# (printf %s "$key" | sha256sum | cut -c1-16) to tell which key the enclave holds.
#
# Environment:
#   PERMAILY_API          base URL of the enclave (default https://api.permaily.com)
#   PERMAILY_ADMIN_TOKEN  admin token (default: ~/.config/permaily/admin-token)
#   PERMAILY_CA_FILE      extra CA certificate, for a staging image
#   PERMAILY_PCRS         pcrs.json of the release the enclave runs, and/or PERMAILY_RELEASE, its tag
#                         (alone, the release's pcrs.json authenticated by its build provenance, that very file):
#                         nothing is sent before permaily-verify attests the enclave against it
#   PERMAILY_VERIFY       permaily-verify binary (default: built from the commit PERMAILY_RELEASE's build provenance
#                         names, never from its tag, fetched into a private repository and built in a fixed Go
#                         environment; or else from this checkout, trusted as the running release's)
#   PERMAILY_DEV_UNVERIFIED=1  for an enclave in dev mode on this machine (scripts/attested.sh)
#
# Needs curl and jq; go and git 2.32 or later unless PERMAILY_VERIFY is set; gh with PERMAILY_RELEASE,
# unless PERMAILY_VERIFY and PERMAILY_PCRS are both set.
set -euo pipefail

usage() { sed -n '2,35p' "$0"; exit 2; }

action=${1:-status}
case $action in
  status) [[ $# -le 1 ]] || usage ;;
  set) [[ $# -eq 1 || $# -eq 2 ]] || usage ;;
  remove) [[ $# -eq 1 ]] || usage ;;
  *) usage ;;
esac
key_file=${2:-}
if [[ -n $key_file ]]; then
  # Not named back: a key given here by mistake would be printed.
  [[ -f $key_file && -r $key_file ]] || { echo "cannot read that file; the key is never an argument (see scripts/model-key.sh --help)" >&2; exit 2; }
  # A key in a file others can read is not kept secret: refused before anything runs.
  # ls reads the mode the same way on Linux and macOS, where stat and find differ.
  # shellcheck disable=SC2012
  perms=$(ls -lnL -- "$key_file" | cut -c5-10)
  [[ $perms == ------ ]] || { echo "$key_file can be read by others: make it mode 600 (chmod 600 $key_file)" >&2; exit 2; }
fi

api=${PERMAILY_API:-https://api.permaily.com}
# What is written here holds the key for a moment: nobody else reads it.
umask 077
# shellcheck source=scripts/attested.sh
source "$(dirname "$0")/attested.sh"
work=$(mktemp -d)
trap attested_cleanup EXIT
load_token
attest

# ask <method> [file with the body] writes the answer to $work/response.json
# and its status to $code.
ask() {
  code=$(enclave_curl --token -o "$work/response.json" -w '%{http_code}' -X "$1" \
    ${2:+-H "Content-Type: application/json" --data-binary "@$2"} "$api/v1/admin/model-key") || exit 1
}

# refused prints why the enclave said no: its reason, and nothing else of the
# answer, which is never printed whole after a request that held the key.
refused() {
  enclave_error "$work/response.json"
  echo "the enclave at $api $1 (HTTP $code)" >&2
  exit 1
}

# said prints the fixed fields of what the enclave says of the key: whether
# one is set, since when, and its fingerprint, each only in its own form.
said() {
  jq -r --arg api "$api" '
    def fp: (.fingerprint | strings | select(test("^[0-9a-f]{16}$"))) // "(none given)";
    def at: (.set_at | strings | select(test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}")) | .[0:16] | sub("T"; " ") + " UTC") // "(a time not given)";
    if .set == true then "a key is set at \($api), since \(at), fingerprint \(fp)"
    else "no key is set at \($api): the model is off" end' "$work/response.json" >&2
}

case $action in
  status)
    ask GET
    [[ $code == 200 ]] || refused "did not say whether a key is set"
    said
    ;;
  set)
    if [[ -n $key_file ]]; then
      cat -- "$key_file" > "$work/key"
    elif [[ -t 0 ]]; then
      read_secret "The model's API key: " "$work/key"
    else
      cat > "$work/key"
    fi
    # The file may end with a line break, which is no part of the key.
    jq -n --rawfile key "$work/key" '{key: ($key | sub("[\r\n]+$"; ""))}' > "$work/body.json"
    rm -f "$work/key"
    ask PUT "$work/body.json"
    rm -f "$work/body.json"
    [[ $code == 200 ]] || refused "did not take the key"
    said
    ;;
  remove)
    ask DELETE
    [[ $code == 204 ]] || refused "did not remove the key"
    echo "the key is removed at $api: the model is off from then." >&2
    ;;
esac
