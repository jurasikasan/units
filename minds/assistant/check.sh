#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"

PASS=0
FAIL=0
ERRORS=""

run() {
  local label="$1"; shift
  if output=$("$@" 2>&1); then
    echo "  ✓ $label"
    PASS=$((PASS + 1))
  else
    echo "  ✗ $label"
    ERRORS="$ERRORS\n--- $label ---\n$output"
    FAIL=$((FAIL + 1))
  fi
}

gofmt_check() {
  local bad
  bad=$(gofmt -l .)
  [ -z "$bad" ] && return 0
  echo "not gofmt'd:"
  echo "$bad"
  return 1
}

run "build" go build ./...
run "vet"   go vet ./...
run "fmt"   gofmt_check

if command -v golangci-lint &>/dev/null; then
  run "lint" golangci-lint run ./...
fi

# Against a fake OpenAI endpoint: the order of events, a model that fails,
# a client that leaves mid-answer, who may ask, what is read of a message.
run "tests" go test -race -count=1 ./...
run "unit"  python3 ../../tools/check_unit.py .

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf "%b\n" "$ERRORS"
  echo "FAIL ($PASS passed, $FAIL failed)"
  exit 1
fi
echo "OK ($PASS checks)"
