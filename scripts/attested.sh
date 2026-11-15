# shellcheck shell=bash disable=SC2154 # $api and $work are set by the script that sources this
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
#   attest                     checks $api, verifies it and remembers its key; exits when it does not verify
#   load_token                 sets $token, the admin token
#   read_secret <prompt> <file>  asks for a secret without showing it, into a file
#   enclave_curl [--token] [curl args]
#                              curl to the enclave, only to that key; never follows a redirect;
#                              --token adds the admin token
#   with_token <command...>    runs a command with PERMAILY_ADMIN_TOKEN set to the token
#   enclave_error <file>       prints the reason in an error answer (error.message), and nothing else
#
# Under bash -x, each of these that touches the token or a secret turns xtrace
# off while it runs: nothing of them is traced. The callers never expand
# $token themselves.
#
# The caller sets $api (the base URL; attest rewrites it without a trailing /)
# and $work (a private directory it removes).
#
# Environment:
#   PERMAILY_API      https://<host> or https://<host>:<port>, with or without
#                     a / at the end, in lowercase: no user or password, path,
#                     query, fragment or percent-encoding. Anything else is
#                     refused before anything runs.
#   PERMAILY_PCRS     pcrs.json of the release the enclave runs, from your own
#                     build (scripts/release-image.sh <tag> <dir>, or
#                     scripts/verify.sh): what the enclave must measure
#   PERMAILY_RELEASE  the tag of that release. The verifier is built from that
#                     tag's source; without PERMAILY_PCRS it also downloads the
#                     release's pcrs.json from GitHub (weaker: trusts CI, as
#                     -release does). Set both for the strongest check.
#   PERMAILY_VERIFY   permaily-verify binary; used as it is, whatever else is set
#   PERMAILY_CA_FILE  extra CA certificate, for a staging image
#   PERMAILY_DEV_UNVERIFIED=1
#                     for an enclave in dev mode, which has no attestation to
#                     verify: sends without verifying, only to localhost,
#                     127.0.0.1, [::1], *.localhost or *.test, and only to this
#                     machine: a name is connected to 127.0.0.1 whatever DNS
#                     says, and no proxy is used. Any value but 1 is refused.
#
# The verifier must be the running release's: it checks the Sigstore trust
# root the enclave reports against the one compiled into itself, and one built
# from a later source refuses an older release (ADR 0029). Without
# PERMAILY_VERIFY or PERMAILY_RELEASE it is built from this checkout, with a
# warning that the checkout must be that release's source.

# What attest sets, cleared first: nothing of it comes from the environment.
pin="" attested_key="" unverified_dev="" verifier="" enclave_ca="" enclave_host="" enclave_port=""
dev_route=()

# attest checks $api, runs permaily-verify against it and sets $pin, the
# sha256 of the SubjectPublicKeyInfo it attested, in curl's format.
attest() {
  enclave_origin
  # The CA file by its absolute path, so a script that changes directory
  # later still finds it.
  enclave_ca=""
  if [[ -n ${PERMAILY_CA_FILE:-} ]]; then
    enclave_ca=$(cd "$(dirname "$PERMAILY_CA_FILE")" && pwd)/$(basename "$PERMAILY_CA_FILE")
  fi
  case ${PERMAILY_DEV_UNVERIFIED:-} in
    "") ;;
    1)
      attest_dev
      return 0
      ;;
    *)
      echo "PERMAILY_DEV_UNVERIFIED is 1, for an enclave in dev mode, or unset; it is set to something else. Nothing was sent." >&2
      exit 2
      ;;
  esac
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
  enclave_verifier
  local ca=() status=0
  [[ -z $enclave_ca ]] || ca=(-ca-file "$enclave_ca")
  "$verifier" "${measured[@]}" ${ca[@]+"${ca[@]}"} -json "$api" > "$work/verified.json" || status=$?

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
  # shellcheck disable=SC2001 # ${key//../} cannot refer to what it matched
  bytes=$(sed 's/../\\x&/g' <<< "$key")
  # shellcheck disable=SC2059 # the escapes are the point
  pin="sha256//$(printf "$bytes" | base64 | tr -d '\n')"
  attested_key=$key
  echo "Verified the enclave at $api: it runs the measured image, with TLS key sha256 $key. The requests go only to that key." >&2
}

# enclave_origin parses $api with a deliberately narrow grammar, the same in
# both modes: https://, a host, an optional port and an optional /. The host is
# a lowercase DNS name whose last label starts with a letter, a dotted IPv4
# address or [::1]. What curl or the verifier could read another way (a user
# before @, an encoded delimiter, a path) is refused, and $api is not printed:
# it may hold a password. It sets $api without the /, $enclave_host and
# $enclave_port.
enclave_origin() {
  local refused="PERMAILY_API must be https://<host> or https://<host>:<port>, with or without a / at the end, in lowercase: no user or password, path, query, fragment or percent-encoding. Nothing was sent."
  if [[ $api != https://* ]]; then
    echo "The token and secrets are sent only over TLS: PERMAILY_API must start with https://. Nothing was sent." >&2
    exit 2
  fi
  local rest=${api#https://} host port=""
  rest=${rest%/}
  local with_port=':([1-9][0-9]{0,4})$'
  if [[ $rest =~ $with_port ]]; then
    port=${BASH_REMATCH[1]}
    host=${rest%:*}
    if ((port > 65535)); then
      echo "$refused" >&2
      exit 2
    fi
  else
    host=$rest
  fi
  local octet='(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])'
  local ipv4="^$octet(\\.$octet){3}\$"
  local label='[a-z0-9]([a-z0-9-]*[a-z0-9])?'
  local name="^($label\\.)*[a-z]([a-z0-9-]*[a-z0-9])?\$"
  local long='[^.]{64}'
  if [[ $host != '[::1]' && ! $host =~ $ipv4 ]] && [[ ! $host =~ $name || $host =~ $long || ${#host} -gt 253 ]]; then
    echo "$refused" >&2
    exit 2
  fi
  api=https://$host${port:+:$port}
  enclave_host=$host
  enclave_port=${port:-443}
}

# attest_dev lets a request go without verifying, to an enclave in dev mode on
# this machine only: the names that never lead to production, connected to
# loopback whatever DNS, a hosts file or a proxy variable says.
attest_dev() {
  if [[ ! $enclave_host =~ ^(localhost|127\.0\.0\.1|\[::1\]|.+\.localhost|.+\.test)$ ]]; then
    echo "PERMAILY_DEV_UNVERIFIED is for an enclave in dev mode on this machine: localhost, 127.0.0.1, [::1], a .localhost or a .test name, not $enclave_host. Nothing was sent." >&2
    exit 2
  fi
  dev_route=(--noproxy '*')
  case $enclave_host in
    # An address is connected to as it is; a name is never looked up.
    127.0.0.1 | '[::1]') ;;
    *) dev_route+=(--resolve "$enclave_host:$enclave_port:127.0.0.1") ;;
  esac
  pin="" attested_key="" unverified_dev=1
  echo "Not verified (PERMAILY_DEV_UNVERIFIED): $enclave_host is taken for an enclave in dev mode on this machine." >&2
}

# enclave_verifier sets $verifier: PERMAILY_VERIFY, or permaily-verify built
# with go from the source of PERMAILY_RELEASE (in a temporary git worktree of
# its tag, removed after), or from this checkout.
enclave_verifier() {
  if [[ -n ${PERMAILY_VERIFY:-} ]]; then
    verifier=$PERMAILY_VERIFY
    case $verifier in
      /*) ;;
      */*) verifier=$(pwd)/$verifier ;;
      *) verifier=$(command -v "$verifier") || { echo "No $PERMAILY_VERIFY to run (PERMAILY_VERIFY). Nothing was sent." >&2; exit 2; } ;;
    esac
    return 0
  fi
  local root tag=${PERMAILY_RELEASE:-}
  root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
  if [[ -n $tag && ! $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "PERMAILY_RELEASE is the tag of a release, such as v0.14.0. Nothing was sent." >&2
    exit 2
  fi
  if ! command -v go >/dev/null; then
    echo "No verifier: install go, or set PERMAILY_VERIFY to a permaily-verify binary. Nothing was sent." >&2
    exit 2
  fi
  verifier=$work/permaily-verify
  local source=$root status=0
  if [[ -n $tag ]]; then
    if ! git -C "$root" rev-parse -q --verify "refs/tags/$tag^{commit}" >/dev/null; then
      git -C "$root" fetch -q --no-tags origin "refs/tags/$tag:refs/tags/$tag" || {
        echo "No tag $tag here or at origin, to build its permaily-verify. Nothing was sent." >&2
        exit 2
      }
    fi
    source=$work/verifier-source
    git -C "$root" worktree add -q --detach "$source" "refs/tags/$tag^{commit}" || {
      echo "Could not check out $tag to build its permaily-verify. Nothing was sent." >&2
      exit 2
    }
  else
    echo "permaily-verify is built from this checkout ($(git -C "$root" rev-parse --short HEAD 2>/dev/null || echo "not a git checkout")): it must be the source of the release the enclave runs, or its Sigstore check refuses that release. Set PERMAILY_RELEASE to the release's tag to build that one, or PERMAILY_VERIFY." >&2
  fi
  # Without VCS stamping, which fails in a git worktree and the verifier does not read.
  go build -C "$source" -buildvcs=false -o "$verifier" ./cmd/permaily-verify || status=$?
  [[ $source == "$root" ]] || git -C "$root" worktree remove --force "$source" || true
  if [[ $status -ne 0 ]]; then
    echo "Could not build permaily-verify${tag:+ of $tag}. Nothing was sent." >&2
    exit 2
  fi
  [[ -z $tag ]] || echo "permaily-verify built from the source of $tag ($(git -C "$root" rev-parse --short "refs/tags/$tag^{commit}"))." >&2
}

# load_token sets $token: PERMAILY_ADMIN_TOKEN, or ~/.config/permaily/admin-token.
load_token() {
  local traced=${-//[^x]/}
  { set +x; } 2>/dev/null
  token=${PERMAILY_ADMIN_TOKEN:-}
  if [[ -z $token ]]; then
    token=$(cat "${HOME}/.config/permaily/admin-token") || {
      echo "No admin token: set PERMAILY_ADMIN_TOKEN, or write it to ~/.config/permaily/admin-token. Nothing was sent." >&2
      exit 2
    }
  fi
  [[ -z $traced ]] || set -x
}

# read_secret asks for a secret on the terminal without showing it, and writes
# it to a file. read and printf are builtins: it is in no process's arguments.
read_secret() {
  local traced=${-//[^x]/} secret
  { set +x; } 2>/dev/null
  IFS= read -rs -p "$1" secret
  echo >&2
  printf '%s' "$secret" > "$2"
  [[ -z $traced ]] || set -x
}

# with_token runs a command (another of these scripts) with the token in
# PERMAILY_ADMIN_TOKEN, in its environment only.
with_token() {
  local traced=${-//[^x]/} status=0
  { set +x; } 2>/dev/null
  PERMAILY_ADMIN_TOKEN=$token "$@" || status=$?
  [[ -z $traced ]] || set -x
  return "$status"
}

# enclave_curl runs curl to the enclave: TLS only, the CA and the name checked,
# the key the one attest attested, no redirect followed, and no ~/.curlrc
# (-q), which could turn a check off. When the connection is to another key,
# curl stops before it sends the request. In dev mode it connects to loopback,
# without a proxy. With --token, the admin token goes in a header read from a
# pipe: it is in no process's arguments.
enclave_curl() {
  local traced=${-//[^x]/}
  { set +x; } 2>/dev/null
  local auth=""
  if [[ ${1:-} == --token ]]; then
    auth=1
    shift
  fi
  local route=()
  if [[ -n ${pin:-} ]]; then
    route=(--pinnedpubkey "$pin")
  elif [[ -n ${unverified_dev:-} ]]; then
    route=("${dev_route[@]}")
  else
    echo "internal error: enclave_curl before attest. Nothing was sent." >&2
    exit 2
  fi
  local options=(-q -sS --proto '=https' --max-redirs 0 "${route[@]}") status=0
  [[ -z $enclave_ca ]] || options+=(--cacert "$enclave_ca")
  if [[ -n $auth ]]; then
    curl "${options[@]}" -H @<(printf 'Authorization: Bearer %s' "$token") "$@" || status=$?
  else
    curl "${options[@]}" "$@" || status=$?
  fi
  if [[ $status -eq 90 ]]; then
    echo "The server at $api answered with a TLS key other than the one attested ($attested_key): nothing was sent to it." >&2
  elif [[ $status -ne 0 ]]; then
    echo "The request to $api did not complete (curl exit $status)." >&2
  fi
  [[ -z $traced ]] || set -x
  return "$status"
}

# enclave_error prints error.message of an answer, without control characters
# and cut at 300 characters: never the rest of the answer, which could echo
# what was sent.
enclave_error() {
  jq -r '(.error? | objects | .message? | strings) // empty | gsub("[[:cntrl:]]"; " ") | .[0:300]' "$1" >&2 2>/dev/null || true
}
