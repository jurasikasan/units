#!/bin/bash
set -euo pipefail

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

# gofmt -l exits 0 whatever it finds; it has to be turned into a failure.
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
run "unit"  python3 ../scripts/check_unit.py .

if command -v golangci-lint &>/dev/null; then
  run "lint" golangci-lint run ./...
fi

# hearing, sight and speaking are fakes in the tests (httptest MCP servers
# built with the SDK), each answering only the tools CONTRACT.md names. The
# streams are a real Redis: what crosses them is the point. One is started
# here when nobody named one and docker is there; without either, the stream
# tests skip and say so.
RD=""
if [ -z "${BODY_TEST_REDIS_ADDR:-}" ] && command -v docker >/dev/null 2>&1; then
  RD=$(docker run -d --rm -p 127.0.0.1::6379 redis:7-alpine 2>/dev/null || true)
  if [ -n "$RD" ]; then
    trap 'docker rm -f "$RD" >/dev/null 2>&1' EXIT
    for _ in $(seq 60); do docker exec "$RD" redis-cli ping >/dev/null 2>&1 && break; sleep 0.5; done
    export BODY_TEST_REDIS_ADDR="$(docker port "$RD" 6379 | head -1)"
  fi
fi
[ -n "${BODY_TEST_REDIS_ADDR:-}" ] || echo "  - no Redis: the live stream tests will skip"

run "test" go test -race -count=1 ./...

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf "%b\n" "$ERRORS"
  echo "FAIL ($PASS passed, $FAIL failed)"
  exit 1
fi
echo "OK ($PASS checks)"
