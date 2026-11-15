#!/usr/bin/env bash
# The fuzz targets, each with its number of executions, in groups that take
# about three minutes each on a GitHub runner. CI runs one group per leg of
# its fuzz matrix (.github/workflows/ci.yml); `make fuzz` runs them all.
#
#   scripts/fuzz.sh            every target, group after group
#   scripts/fuzz.sh <group>    one group's targets
#   scripts/fuzz.sh --groups   the groups' names, one a line
#
# A number of executions, not a duration: Go's fuzzer sometimes fails with
# "context deadline exceeded" when a time limit ends mid-execution. The
# seconds are one target's on a runner of develop (2026-11-15), to keep the
# groups balanced when a target is added.
set -euo pipefail
cd "$(dirname "$0")/.."

# group package target executions seconds
targets='
verify    ./pkg/attestation  FuzzVerify          500000   22
verify    ./pkg/attestation  FuzzParsePCRFile    400000   35
verify    ./pkg/kmspolicy    FuzzAnalyze         1000000  18
verify    ./pkg/provenance   FuzzVerify          300000   17
verify    ./pkg/provenance   FuzzVerifyRekor2    200000   8
verify    ./pkg/provenance   FuzzTimestamp       300000   56
verify    ./pkg/provenance   FuzzTrustUpdate     200000   13
verify    ./internal/keys    FuzzCMS             500000   23
accounts  ./internal/inbox   FuzzParse           150000   10
accounts  ./internal/inbox   FuzzMessageText     150000   9
accounts  ./internal/inbox   FuzzWalkParts       150000   9
accounts  ./internal/webauthn FuzzRegistered     400000   10
accounts  ./internal/webauthn FuzzAsserted       400000   11
accounts  ./internal/idp     FuzzIDToken         400000   29
accounts  ./internal/idp     FuzzClaims          300000   14
accounts  ./internal/idp     FuzzKeys            400000   31
accounts  ./internal/idp     FuzzTokenAnswer     200000   9
accounts  ./internal/idp     FuzzGitHubUser      100000   7
accounts  ./internal/idp     FuzzGitHubEmails    300000   15
accounts  ./internal/idp     FuzzMetaUser        300000   14
mailhtml  ./internal/mailhtml FuzzSanitise       100000   23
mailhtml  ./internal/mailhtml FuzzCSS            1000000  103
mailhtml  ./internal/mailhtml FuzzImageStructure 1000000  27
query-a   ./internal/query   FuzzParse           300000   94
query-a   ./internal/query   FuzzEval            200000   61
query-b   ./internal/query   FuzzNormalise       500000   57
query-b   ./internal/query   FuzzSentenceReadsBack 300000 94
'

groups() { awk 'NF && !seen[$1]++ { print $1 }' <<<"$targets"; }

if [[ ${1:-} == --groups ]]; then
  groups
  exit 0
fi
want=${1:-}
if [[ -n $want ]] && ! groups | grep -qx -- "$want"; then
  echo "fuzz.sh: no group named $want (groups: $(groups | tr '\n' ' '))" >&2
  exit 2
fi
while read -r group pkg target execs _; do
  [[ -z $group || (-n $want && $group != "$want") ]] && continue
  echo "== $pkg $target ${execs}x"
  go test -run='^$' -fuzz="^${target}\$" -fuzztime="${execs}x" "$pkg"
done <<<"$targets"
