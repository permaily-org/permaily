# Sourced by every operator script that sends the admin token or a secret to
# the enclave (scripts/provider.sh, accounts.sh, invite.sh, handoff.sh and
# sigstore-trust-root.sh --send). It is not run on its own.
#
# A certificate that a CA checks only proves the name: someone who got another
# valid certificate for api.permaily.com, and the network between, would get
# whatever a plain curl sends. So nothing is sent before permaily-verify has
# attested the enclave at $api, in this same run, and every request then goes
# only over a connection with the TLS key it attested (curl --pinnedpubkey),
# with the CA and hostname checks as well. If the verification fails, nothing
# is sent at all.
#
#   attest                     verifies $api and remembers its key; exits when it does not verify
#   enclave_curl [curl args]   curl to the enclave, only to that key; never follows a redirect
#   enclave_error <file>       prints the reason in an error answer (error.message), and nothing else
#
# The caller sets $api (the base URL) and $work (a private directory it removes).
#
# Environment:
#   PERMAILY_PCRS     pcrs.json of the release the enclave runs, from your own
#                     build (scripts/release-image.sh <tag> <dir>, or
#                     scripts/verify.sh): what the enclave must measure
#   PERMAILY_RELEASE  or the tag of that release: the verifier downloads its
#                     pcrs.json from GitHub (weaker: trusts CI, as -release does)
#   PERMAILY_VERIFY   permaily-verify binary (default: built from this checkout with go)
#   PERMAILY_CA_FILE  extra CA certificate, for a staging image
#   PERMAILY_DEV_UNVERIFIED=1
#                     for an enclave in dev mode, which has no attestation to
#                     verify: sends without verifying, and only to localhost,
#                     127.0.0.1, [::1], *.localhost or *.test, names that never
#                     lead to production

# attest runs permaily-verify against $api and sets $pin, the sha256 of the
# SubjectPublicKeyInfo it attested, in curl's format.
attest() {
  if [[ $api != https://* ]]; then
    echo "The token and secrets are sent only over TLS: PERMAILY_API must start with https://. Nothing was sent." >&2
    exit 2
  fi
  # The CA file by its absolute path, so a script that changes directory
  # later still finds it.
  enclave_ca=""
  if [[ -n ${PERMAILY_CA_FILE:-} ]]; then
    enclave_ca=$(cd "$(dirname "$PERMAILY_CA_FILE")" && pwd)/$(basename "$PERMAILY_CA_FILE")
  fi
  if [[ -n ${PERMAILY_DEV_UNVERIFIED:-} ]]; then
    local host=${api#https://}
    host=${host%%/*}
    if [[ $host == \[* ]]; then host=${host%%]*}]; else host=${host%%:*}; fi
    host=$(tr "[:upper:]" "[:lower:]" <<< "$host")
    if [[ ! $host =~ ^(localhost|127\.0\.0\.1|\[::1\]|.+\.localhost|.+\.test)$ ]]; then
      echo "PERMAILY_DEV_UNVERIFIED is for an enclave in dev mode, on localhost or a .test name, not $host. Nothing was sent." >&2
      exit 2
    fi
    pin="" attested_key="" unverified_dev=1
    echo "Not verified (PERMAILY_DEV_UNVERIFIED): $host is taken for an enclave in dev mode." >&2
    return 0
  fi
  local measured
  if [[ -n ${PERMAILY_PCRS:-} ]]; then
    measured=(-pcrs "$PERMAILY_PCRS")
  elif [[ -n ${PERMAILY_RELEASE:-} ]]; then
    measured=(-release "$PERMAILY_RELEASE")
  else
    echo "Nothing is sent before the enclave at $api is verified, and that needs what it must measure:" >&2
    echo "set PERMAILY_PCRS to the pcrs.json of the release it runs (scripts/release-image.sh <tag> <dir> leaves it in <dir>)," >&2
    echo "or PERMAILY_RELEASE to that release's tag. Nothing was sent." >&2
    exit 2
  fi
  local verify=${PERMAILY_VERIFY:-}
  if [[ -z $verify ]]; then
    if ! command -v go >/dev/null; then
      echo "No verifier: install go, or set PERMAILY_VERIFY to a permaily-verify binary. Nothing was sent." >&2
      exit 2
    fi
    verify=$work/permaily-verify
    # Without VCS stamping, which fails in a git worktree and the verifier does not read.
    if ! go build -C "$(dirname "${BASH_SOURCE[0]}")/.." -buildvcs=false -o "$verify" ./cmd/permaily-verify; then
      echo "Could not build permaily-verify. Nothing was sent." >&2
      exit 2
    fi
  fi
  local ca=() status=0
  [[ -z $enclave_ca ]] || ca=(-ca-file "$enclave_ca")
  "$verify" "${measured[@]}" ${ca[@]+"${ca[@]}"} -json "$api" > "$work/verified.json" || status=$?

  # One host, the one asked about, that passed every check, and its key.
  local key
  key=$(jq -r --arg api "$api" 'if .ok == true and (.hosts | length) == 1 and .hosts[0].url == $api and .hosts[0].ok == true
    then .hosts[0].tls_spki_sha256 // "" else "" end' "$work/verified.json" 2>/dev/null || true)
  if [[ $status -ne 0 || ! $key =~ ^[0-9a-f]{64}$ ]]; then
    jq -r '.hosts[]? | select(.ok != true) | "\(.url): \(.error)"' "$work/verified.json" 2>/dev/null | tr -d '\000-\010\013-\037\177' >&2 || true
    echo "permaily-verify did not verify the enclave at $api. Nothing was sent." >&2
    exit 1
  fi
  # curl wants the hash in base64; the verifier says it in hex.
  local bytes
  bytes=$(sed 's/../\\x&/g' <<< "$key")
  # shellcheck disable=SC2059 # the escapes are the point
  pin="sha256//$(printf "$bytes" | base64)"
  attested_key=$key
  echo "Verified the enclave at $api: it runs the measured image, with TLS key sha256 $key. The requests go only to that key." >&2
}

# enclave_curl runs curl to the enclave: TLS only, the CA and the name checked,
# the key the one attest attested, no redirect followed, and no ~/.curlrc
# (-q), which could turn a check off. When the connection is to another key,
# curl stops before it sends the request.
enclave_curl() {
  local key=()
  if [[ -n ${pin:-} ]]; then
    key=(--pinnedpubkey "$pin")
  elif [[ -z ${unverified_dev:-} ]]; then
    echo "internal error: enclave_curl before attest. Nothing was sent." >&2
    exit 2
  fi
  local ca=() status=0
  [[ -z $enclave_ca ]] || ca=(--cacert "$enclave_ca")
  curl -q -sS --proto '=https' --max-redirs 0 ${key[@]+"${key[@]}"} ${ca[@]+"${ca[@]}"} "$@" || status=$?
  if [[ $status -eq 90 ]]; then
    echo "The server at $api answered with a TLS key other than the one attested ($attested_key): nothing was sent to it." >&2
  elif [[ $status -ne 0 ]]; then
    echo "The request to $api did not complete (curl exit $status)." >&2
  fi
  return "$status"
}

# enclave_error prints error.message of an answer, without control characters
# and cut at 300 characters: never the rest of the answer, which could echo
# what was sent.
enclave_error() {
  jq -r '(.error? | objects | .message? | strings) // empty | gsub("[[:cntrl:]]"; " ") | .[0:300]' "$1" >&2 2>/dev/null || true
}
