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
#   PERMAILY_RELEASE  the tag of that release. The verifier is built from the
#                     commit that release's build provenance names (below).
#                     Without PERMAILY_PCRS, the release's pcrs.json is
#                     downloaded once, authenticated by that provenance, and
#                     that same file is what the verifier checks (-pcrs; never
#                     -release, which would download it again): weaker than
#                     your own build, as it trusts CI. Set both for the
#                     strongest check; PERMAILY_PCRS, given, is what is checked.
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
# from a later source refuses an older release (ADR 0029). The verifier decides
# which key the secrets go to, so its source is never taken from a tag's name:
# for a named release it is built from the commit that the release's pcrs.json
# build provenance names (gh attestation verify, for permaily-org/permaily, its
# release workflow and main), when the release's tag at
# github.com/permaily-org/permaily names that same commit; never a tag here or
# at origin. That commit is fetched by its id from there into a new private
# repository in $work and checked out there: nothing of this checkout is used
# (its objects, replacement refs, hooks, configuration or attributes), no GIT_*
# variable reaches git, replacement objects are off, and the checkout reads no
# git configuration at all; only the fetch reads yours (a proxy, a mirror),
# since what it brings is checked by its id. It is compiled with a fixed Go
# environment (verifier_build). Without gh, or offline, or for a tag with no
# release, nothing is built: set PERMAILY_VERIFY. Without PERMAILY_VERIFY or
# PERMAILY_RELEASE it is built from this checkout, with a warning: that trusts
# the checkout, uncommitted changes included, to be the running release's
# source. None of this defends against a compromised go, git or system: they
# are trusted, as is the operator's computer.
#
# The caller sets its EXIT trap to attested_cleanup, which removes $work (and
# with it the verifier's private repository) when a run stops.

# The repository whose release workflow, on main, builds every release.
release_repository=permaily-org/permaily
release_url=https://github.com/$release_repository

# What attest sets, cleared first: nothing of it comes from the environment.
pin="" attested_key="" unverified_dev="" verifier="" enclave_ca="" enclave_host="" enclave_port=""
release_tag="" release_commit="" release_file=""
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
    # The release's pcrs.json, authenticated by its build provenance, and those
    # very bytes are what the verifier checks: -release would download it again,
    # and what it got then is what nothing authenticated.
    release_pcrs "$PERMAILY_RELEASE" "$work/release"
    measured=(-pcrs "$release_file")
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
# from the commit PERMAILY_RELEASE's build provenance names (in a private
# repository in $work), or from this checkout.
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
  release_tag_format "$tag"
  if ! command -v go >/dev/null; then
    echo "No verifier: install go, or set PERMAILY_VERIFY to a permaily-verify binary. Nothing was sent." >&2
    exit 2
  fi
  verifier=$work/permaily-verify
  local source=$root
  if [[ -n $tag ]]; then
    # The commit the release's provenance names; attest or handoff.sh may have it already.
    [[ $release_tag == "$tag" && -n $release_commit ]] || release_pcrs "$tag" "$work/release"
    local commit=$release_commit
    source=$work/verifier-source
    verifier_checkout "$commit" "$source" || {
      echo "Could not fetch and check out commit $commit, the source of $tag, from $release_url. Nothing was sent." >&2
      exit 2
    }
  else
    echo "permaily-verify is built from this checkout ($(git -C "$root" rev-parse --short HEAD 2>/dev/null || echo "not a git checkout")), uncommitted changes included: it must be the source of the release the enclave runs, or its Sigstore check refuses that release, and you trust it as that source. Set PERMAILY_RELEASE to the release's tag to build the source its build provenance names, or PERMAILY_VERIFY." >&2
  fi
  verifier_build "$source" "$verifier" || {
    echo "Could not build permaily-verify${tag:+ of $tag}. Nothing was sent." >&2
    exit 2
  }
  [[ -z $tag ]] || echo "permaily-verify built from commit $commit, the source of $tag by its build provenance." >&2
}

# release_git [--no-config] <git arguments> runs git with no GIT_* variable from
# the environment (none can point it at another repository, index, worktree,
# object store or configuration), replacement objects off, no hook and no
# prompt. With --no-config it reads no configuration file either (system,
# global, an attributes file): only the repository's own, which is ours.
release_git() {
  (
    for name in $(compgen -e); do
      case $name in GIT_*) unset "$name" ;; esac
    done
    export GIT_NO_REPLACE_OBJECTS=1 GIT_TERMINAL_PROMPT=0
    if [[ $1 == --no-config ]]; then
      shift
      export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
      set -- -c core.attributesFile=/dev/null "$@"
    fi
    exec git --no-replace-objects -c core.hooksPath=/dev/null "$@"
  )
}

# verifier_checkout <commit> <dir> fetches <commit> by its id from the
# repository into a new repository at <dir> and checks it out there, so <dir>
# holds exactly that commit's tree. A working tree, not git archive: the files
# are what a checkout of the commit writes, as in the release workflow, the
# commit's own .gitattributes applied and nothing else (archive would also
# apply its export-ignore and export-subst); no filter runs, as no
# configuration is read to define one; and the new repository has no hook,
# attributes file or replacement ref. What arrives is checked by its object id.
verifier_checkout() {
  local commit=$1 dir=$2
  mkdir "$dir" &&
    release_git --no-config init -q --template= "$dir" &&
    release_git -C "$dir" fetch -q --depth=1 --no-tags "$release_url" "$commit" &&
    release_git --no-config -C "$dir" -c advice.detachedHead=false checkout -q --detach "$commit" &&
    [[ $(release_git --no-config -C "$dir" rev-parse HEAD) == "$commit" ]] || return 1
}

# verifier_build <source> <out> compiles permaily-verify in a fixed Go
# environment, so nothing outside <source> changes what is compiled: no GO* or
# CGO_* variable is kept but GOTMPDIR and GOPROXY; the module and build caches
# are new and empty, next to <out> (go checks a module it downloads against
# go.sum, but compiles an extracted module, and reuses a build result, without
# hashing it again, so the operator's caches would be trusted as they are;
# -modcacherw lets the run's cleanup delete them); no
# go.env file (GOENV=off), so no GOFLAGS: no -overlay, -modfile or -toolexec;
# no workspace (GOWORK=off); GOTOOLCHAIN=auto, so go.mod's toolchain line
# applies (an older go downloads that one, checked against the checksum
# database); no cgo, as the release builds it; -trimpath; and no VCS stamp,
# which the verifier does not read.
verifier_build() {
  (
    for name in $(compgen -e); do
      case $name in
        GOTMPDIR | GOPROXY) ;;
        GO* | CGO_*) unset "$name" ;;
      esac
    done
    local caches
    caches=$(dirname "$2")/go
    mkdir -p "$caches/mod" "$caches/build" || exit 1
    export GOENV=off GOWORK=off GOTOOLCHAIN=auto CGO_ENABLED=0
    export GOPATH=$caches GOMODCACHE=$caches/mod GOCACHE=$caches/build
    exec go build -C "$1" -modcacherw -trimpath -buildvcs=false -o "$2" ./cmd/permaily-verify
  )
}

# release_tag_format refuses what is not a release's tag (or empty), before it
# reaches git or gh.
release_tag_format() {
  if [[ -n $1 && ! $1 =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "PERMAILY_RELEASE is the tag of a release, such as v0.14.0. Nothing was sent." >&2
    exit 2
  fi
}

# release_pcrs <tag> <dir> downloads the release's pcrs.json into <dir> and
# authenticates it: GitHub's build provenance for it, made by the release
# workflow of permaily-org/permaily on main (gh attestation verify). It sets
# $release_file to that file (the one to verify against: it is never
# downloaded again), $release_commit to the source commit that provenance
# names, which must be the one the release's tag names at
# github.com/permaily-org/permaily (the release workflow tags the commit it
# built), and $release_tag. It exits when any of that cannot be had: nothing is
# sent then.
release_pcrs() {
  local tag=$1 dir=$2
  release_tag_format "$tag"
  release_tag="" release_commit="" release_file=""
  local without="Set PERMAILY_VERIFY to a permaily-verify binary you trust as $tag's. Nothing was sent."
  if ! command -v gh >/dev/null; then
    echo "No gh, to authenticate $tag's build provenance before building its verifier. $without" >&2
    exit 2
  fi
  mkdir -p "$dir"
  rm -f "$dir/pcrs.json"
  gh release download "$tag" --repo "$release_repository" --pattern pcrs.json --dir "$dir" || {
    echo "Could not download the pcrs.json of release $tag of $release_repository. $without" >&2
    exit 2
  }
  gh attestation verify "$dir/pcrs.json" --repo "$release_repository" \
    --signer-workflow "$release_repository/.github/workflows/release.yml" --source-ref refs/heads/main \
    --deny-self-hosted-runners --format json > "$dir/provenance.json" || {
    echo "The pcrs.json of $tag has no build provenance from $release_repository's release workflow on main. $without" >&2
    exit 2
  }
  # Every commit an attestation of this file names (one, unless two releases built the same image).
  local attested
  attested=$(jq -r --arg url "$release_url" '.[].verificationResult.signature.certificate
    | select(.sourceRepositoryURI == $url and .sourceRepositoryRef == "refs/heads/main")
    | .sourceRepositoryDigest | strings | select(test("^[0-9a-f]{40}$"))' "$dir/provenance.json" 2>/dev/null || true)
  if [[ -z $attested ]]; then
    echo "The build provenance of $tag names no commit of $release_url. $without" >&2
    exit 2
  fi
  local refs tagged
  refs=$(release_git ls-remote "$release_url" "refs/tags/$tag" "refs/tags/$tag^{}") || {
    echo "Could not read tag $tag at $release_url. $without" >&2
    exit 2
  }
  # An annotated tag's commit is on its ^{} line.
  tagged=$(awk -v t="refs/tags/$tag^{}" '$2 == t { print $1 }' <<< "$refs")
  [[ -n $tagged ]] || tagged=$(awk -v t="refs/tags/$tag" '$2 == t { print $1 }' <<< "$refs")
  if [[ -z $tagged ]] || ! grep -qx "$tagged" <<< "$attested"; then
    echo "Tag $tag at $release_url names ${tagged:-no commit}, but the build provenance of $tag names $(tr '\n' ' ' <<< "$attested")and nothing else. $without" >&2
    exit 2
  fi
  release_tag=$tag release_commit=$tagged release_file=$dir/pcrs.json
}

# attested_cleanup is the caller's EXIT trap: it removes $work, the verifier's
# private repository with it.
attested_cleanup() {
  rm -rf "$work"
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
