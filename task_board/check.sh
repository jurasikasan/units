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

# The journal's SQL is tested against a real Postgres (internal/thread/
# repository_test.go); one is started here when nobody named one and docker
# is there. Without either, those tests skip and say so.
PG=""
if [ -z "${TASK_BOARD_TEST_DATABASE_URL:-}" ] && command -v docker >/dev/null 2>&1; then
  PG=$(docker run -d --rm -e POSTGRES_USER=t -e POSTGRES_PASSWORD=t -e POSTGRES_DB=t \
       -p 127.0.0.1::5432 postgres:16-alpine 2>/dev/null || true)
  if [ -n "$PG" ]; then
    trap 'docker rm -f "$PG" >/dev/null 2>&1' EXIT
    for _ in $(seq 60); do docker exec "$PG" pg_isready -U t -h 127.0.0.1 >/dev/null 2>&1 && break; sleep 0.5; done
    export TASK_BOARD_TEST_DATABASE_URL="postgres://t:t@$(docker port "$PG" 5432 | head -1)/t?sslmode=disable"
  fi
fi
[ -n "${TASK_BOARD_TEST_DATABASE_URL:-}" ] || echo "  - no Postgres: the live repository tests will skip"

run "test" go test -count=1 -p 1 ./...   # one at a time: the live tests share one database

swag_check() {
  git ls-files --error-unmatch docs/swagger.json >/dev/null 2>&1 || return 0
  local swagbin
  if command -v swag >/dev/null 2>&1; then swagbin=$(command -v swag)
  else go install github.com/swaggo/swag/cmd/swag@latest >/dev/null 2>&1 || { echo "swag not found and could not be installed"; return 1; }
       swagbin=$(go env GOPATH)/bin/swag; fi
  local tmp; tmp=$(mktemp -d) || return 1
  "$swagbin" init -g cmd/app/main.go -o "$tmp" >/dev/null 2>&1 || { echo "swag init failed"; rm -rf "$tmp"; return 1; }
  local rc=0
  for f in swagger.json swagger.yaml; do cmp -s "$tmp/$f" "docs/$f" || rc=1; done
  rm -rf "$tmp"
  [ "$rc" = 0 ] || echo "committed swagger spec is stale — run doc.sh"
  return $rc
}
run "spec" swag_check
run "unit" python3 ../tools/check_unit.py .

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf "%b\n" "$ERRORS"
  echo "FAIL ($PASS passed, $FAIL failed)"
  exit 1
fi
echo "OK ($PASS checks)"
