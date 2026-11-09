#!/usr/bin/env bash
# Runs the Go tests with coverage and fails if a package is below its threshold
# in scripts/coverage-thresholds.txt.
set -euo pipefail
cd "$(dirname "$0")/.."
if ! out=$(go test -count=1 -cover ./... 2>&1); then
  echo "$out"
  exit 1
fi
echo "$out" | grep -v "no test files"
fail=0
while read -r pkg min; do
  [[ -z $pkg || $pkg == \#* ]] && continue
  got=$(echo "$out" | awk -v p="$pkg" '$2 == p { for (i = 1; i <= NF; i++) if ($i ~ /%$/) { sub(/%/, "", $i); print $i } }')
  if [[ -z $got ]]; then
    echo "coverage: no result for $pkg"
    fail=1
    continue
  fi
  if awk -v g="$got" -v m="$min" 'BEGIN { exit !(g < m) }'; then
    echo "coverage: $pkg is ${got}%, below ${min}%"
    fail=1
  fi
done < scripts/coverage-thresholds.txt
exit $fail
