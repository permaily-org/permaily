#!/usr/bin/env bash
# Fetches the trust root Sigstore publishes today, with the TUF metadata that
# proves it, and checks it the way a running enclave would: every TUF root
# signed by the keyholders of the one before, starting from the one compiled
# into pkg/provenance. Nothing here is trusted for being downloaded; the
# verifier decides (permaily-verify -trust-update).
#
#   scripts/sigstore-trust-root.sh            compare; exit 1 when Sigstore
#                                             published a newer one, or one the
#                                             verifier refuses
#   scripts/sigstore-trust-root.sh --send     give the newer one to the running
#                                             enclave, which checks it again; it
#                                             verifies the enclave first, and the
#                                             token goes only to the TLS key that
#                                             verification attested
#                                             (scripts/attested.sh)
#   scripts/sigstore-trust-root.sh --update   replace the files compiled into
#                                             pkg/provenance, for the next release
#   scripts/sigstore-trust-root.sh --fetch DIR   only download into DIR
#
# A newer trust root is not an attack on its own: Sigstore adds logs and
# renews certificates. It means the running enclave needs it before it can
# accept a release signed with what is new (docs/operations.md, "Sigstore
# changed").
#
# Environment:
#   PERMAILY_VERIFY       the verifier to decide with (default: go run
#                         ./cmd/permaily-verify, the one of this checkout; with
#                         --send, the one that attested the enclave, built from
#                         the source of PERMAILY_RELEASE when it is set)
#   PERMAILY_API          base URL of the enclave (default https://api.permaily.com)
#   PERMAILY_ADMIN_TOKEN  admin token (default: ~/.config/permaily/admin-token)
#   PERMAILY_CA_FILE      extra CA certificate, for a staging image
#   PERMAILY_PCRS         with --send: pcrs.json of the release the enclave runs
#                         and/or PERMAILY_RELEASE, its tag, to verify it against
#                         (scripts/attested.sh)
#
# Needs curl and jq, and go (and git with --send) unless PERMAILY_VERIFY is set.
set -euo pipefail

repository=https://tuf-repo-cdn.sigstore.dev
compiled=pkg/provenance
mode=${1:-compare}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
# The admin token goes to the enclave only after it is verified, and only to
# the key that verification attested; nothing is fetched before that either.
if [[ $mode == --send ]]; then
  api=${PERMAILY_API:-https://api.permaily.com}
  # shellcheck source=scripts/attested.sh
  source "$(dirname "$0")/attested.sh"
  load_token
  attest
fi
cd "$(dirname "$0")/.."
dir=$work/tuf
[[ $mode == --fetch ]] && dir=${2:?--fetch needs a directory}
mkdir -p "$dir"

get() { curl -sSf --retry 3 -o "$2" "$repository/$1"; }

# The newest targets metadata, by way of the timestamp and the snapshot.
get timestamp.json "$work/timestamp.json"
snapshot=$(jq -r '.signed.meta["snapshot.json"].version' "$work/timestamp.json")
get "$snapshot.snapshot.json" "$work/snapshot.json"
targets=$(jq -r '.signed.meta["targets.json"].version' "$work/snapshot.json")
get "$targets.targets.json" "$dir/targets.json"
# The trust root it names, under its hash.
hash=$(jq -r '.signed.targets["trusted_root.json"].hashes.sha256' "$dir/targets.json")
[[ $hash =~ ^[0-9a-f]{64}$ ]] || { echo "the targets metadata names no trusted_root.json" >&2; exit 2; }
get "targets/$hash.trusted_root.json" "$dir/trusted_root.json"
# Every TUF root after the one compiled in, until there is no next one. The
# running enclave may have been built with an older one: then from that one.
version=$(jq -r '.signed.version' "$compiled/sigstore_tuf_root.json")
if [[ $mode == --send ]]; then
  running=$(enclave_curl "$api/status" | jq -r '.sigstore.tuf_root_version // empty | numbers')
  if [[ -z $running ]]; then
    echo "The enclave at $api reports no Sigstore trust root: its release is older, and uses the one compiled into it. Nothing to send."
    exit 0
  fi
  if ((running < version)); then version=$running; fi
fi
while curl -sf --retry 3 -o "$dir/$((version + 1)).root.json" "$repository/$((version + 1)).root.json"; do
  version=$((version + 1))
done
rm -f "$dir/$((version + 1)).root.json"
[[ $mode == --fetch ]] && { echo "Fetched TUF targets $targets and TUF roots up to $version into $dir."; exit 0; }

# With --send, the verifier that attested the enclave: the running release's.
decide=() decider=""
if [[ $mode == --send && -n ${verifier:-} ]]; then
  decide=("$verifier") decider=1
elif [[ -n ${PERMAILY_VERIFY:-} ]]; then
  decide=("$PERMAILY_VERIFY") decider=1
elif command -v go >/dev/null; then
  decide=(go run ./cmd/permaily-verify) decider=1
fi
if [[ -n $decider ]]; then
  status=0
  "${decide[@]}" -trust-update "$dir" -json > "$work/result.json" || status=$?
  if [[ $status -ne 0 ]]; then
    "${decide[@]}" -trust-update "$dir" || true
    echo "::error::pkg/provenance refuses the trust root Sigstore publishes today. See \"Sigstore changed\" in docs/operations.md."
    exit 1
  fi
  "${decide[@]}" -trust-update "$dir"
  if [[ $(jq -r .newer "$work/result.json") != true && $mode != --send ]]; then
    exit 0
  fi
elif [[ $mode != --send ]]; then
  echo "no verifier to decide with: install go, or set PERMAILY_VERIFY to a permaily-verify binary" >&2
  exit 2
fi
# With --send and no verifier here, the enclave is the one that decides anyway.

case $mode in
  compare)
    echo
    echo "What changed ('<' compiled in, '>' published today):"
    diff <(jq -S . "$compiled/sigstore_trusted_root.json") <(jq -S . "$dir/trusted_root.json") || true
    echo
    echo "::error::Sigstore published a newer trust root than the one compiled into the enclave. See \"Sigstore changed\" in docs/operations.md."
    exit 1
    ;;
  --update)
    cp "$dir/trusted_root.json" "$compiled/sigstore_trusted_root.json"
    cp "$dir/targets.json" "$compiled/sigstore_tuf_targets.json"
    if [[ -f $dir/$version.root.json ]]; then
      cp "$dir/$version.root.json" "$compiled/sigstore_tuf_root.json"
    fi
    echo "Replaced the files compiled into $compiled. Run the tests, and release."
    ;;
  --send)
    # Each file as it was served, in base64 without line breaks (GNU's or
    # macOS's base64); the roots in order.
    roots=$(for f in $(find "$dir" -name '*.root.json' | sort -V); do base64 < "$f" | tr -d '\n'; echo; done | jq -R . | jq -s .)
    base64 < "$dir/targets.json" | tr -d '\n' > "$work/targets.b64"
    base64 < "$dir/trusted_root.json" | tr -d '\n' > "$work/trusted.b64"
    jq -n --argjson roots "$roots" --rawfile targets "$work/targets.b64" --rawfile trusted "$work/trusted.b64" \
      '{roots: $roots, targets: $targets, trusted_root: $trusted}' > "$work/request.json"
    code=$(enclave_curl --token -o "$work/response.json" -w '%{http_code}' \
      -X POST -H "Content-Type: application/json" \
      --data-binary @"$work/request.json" "$api/v1/admin/trust-root") || exit 1
    case $code in
      200)
        # What the enclave answers, field by field: nothing else of the answer is printed.
        jq -r 'def clean: tostring | gsub("[[:cntrl:]]"; " ");
          "trusted_root.json sha256 \(.trusted_root_sha256 | clean), TUF root \(.tuf_root_version | clean), TUF targets \(.tuf_targets_version | clean)"' "$work/response.json"
        if [[ $(jq -r '.updated_at // empty' "$work/response.json") == "" ]]; then
          echo "The enclave at $api already uses that trust root: it is the one compiled into it."
        else
          echo "The enclave at $api checks releases against that trust root now, until it restarts."
        fi
        ;;
      *)
        enclave_error "$work/response.json"
        echo "the enclave refused the trust root, or the request failed (HTTP $code)" >&2
        exit 1
        ;;
    esac
    ;;
  *)
    sed -n '2,38p' "$0"
    exit 2
    ;;
esac
