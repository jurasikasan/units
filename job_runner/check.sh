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

# `gofmt -l` exits 0 whether or not it finds anything, so it cannot be handed to
# `run` directly — a bare `run "fmt" gofmt -l .` would pass forever. Print the
# offenders too: the gate's output is often the only thing a human reads back
# from an agent lane.
gofmt_check() {
  local bad
  bad=$(gofmt -l .)
  [ -z "$bad" ] && return 0
  echo "not gofmt'd:"
  echo "$bad"
  return 1
}

# `docs/` is committed in this repo, so the generated Swagger spec ships with
# the code. But `swag init` only reads annotations (it has no view of the
# router): an edited `@Param`, a renamed route, or a whole new handler will not
# force a regeneration, and `swag init` exits 0 on a handler that has no
# annotation at all. `build`/`vet`/`test` never cross-check the committed spec
# against the source, so a stale spec sails through every other check — that is
# how a changed `show_drafts` annotation shipped with an unregenerated `docs/`
# on 2026-10-04, and how the no-annotation route case shipped before it.
#
# A lane has no editor hook and no habit of running `doc.sh`; the spec is the
# same class of "writer-produced, nobody-checks" artifact as unformatted Go, and
# it is fixed the same way — by the gate. Regenerate into a throwaway dir with
# the same tool `doc.sh` uses and diff it against the committed spec: it needs
# no router knowledge and does not depend on the author remembering.
swag_check() {
  # Only modules that actually commit their spec owe this; an untracked docs/
  # (like api/) is out of scope.
  git ls-files --error-unmatch docs/swagger.json >/dev/null 2>&1 || return 0

  local swagbin
  if command -v swag >/dev/null 2>&1; then
    swagbin=$(command -v swag)
  else
    if ! go install github.com/swaggo/swag/cmd/swag@latest >/dev/null 2>&1; then
      echo "swag not found and could not be installed (@latest, needs network)"
      return 1
    fi
    swagbin=$(go env GOPATH)/bin/swag
  fi

  local tmp
  if ! tmp=$(mktemp -d); then
    echo "could not create a temp dir to regenerate the spec"
    return 1
  fi

  local swaglog
  if ! swaglog=$("$swagbin" init -g cmd/app/main.go -o "$tmp" 2>&1); then
    rm -rf "$tmp"
    echo "swag init failed:"
    echo "$swaglog"
    return 1
  fi

  # Compare the generated spec (json/yaml) only, not docs.go: swag names the
  # package after the output dir and does not gofmt it, so docs.go is never
  # byte-stable across regenerations. json/yaml are stable (verified identical
  # across runs and versions that doc.sh has used).
  local stale="" f
  for f in swagger.json swagger.yaml; do
    [ -f "docs/$f" ] && [ -f "$tmp/$f" ] || continue
    if ! diff -q "docs/$f" "$tmp/$f" >/dev/null 2>&1; then
      stale="${stale}  - ${f}\n"
    fi
  done
  rm -rf "$tmp"

  if [ -n "$stale" ]; then
    echo "committed swagger spec is stale — source annotations diverge from docs/:"
    printf "%b\n" "$stale"
    echo "  fix: run ./doc.sh, then commit the refreshed docs/"
    return 1
  fi
  return 0
}

run "build" go build ./...
run "vet"   go vet ./...
# There is no `.golangci.yml` anywhere in this repo, so golangci-lint runs its
# default linter set, which does not include gofmt — and `build` and `vet` do not
# look at formatting at all. So nothing in the fleet checked it until 2026-09-28.
# It held anyway, because every writer here gets gofmt from an editor hook for
# free. A lane has no editor, and `ps-deadcode` proved the point on 2026-09-27:
# it deleted a commented-out block, left the blank line behind it, and collected
# a green "OK (4 checks)" on the first unformatted file in the tree.
run "fmt"   gofmt_check

run "spec"  swag_check

if command -v golangci-lint &>/dev/null; then
  run "lint" golangci-lint run ./...
fi

# No -maxdepth: it used to be 4, and a package nested deeper than that made the
# gate skip `go test` entirely while still reporting green. An agent lane hit
# exactly that in document_parser on 2026-09-25 — its only test sat at depth 5.
# A larger number just moves the trap, so there is no number.
if find . -name "*_test.go" | grep -q .; then
  run "test" go test ./...
fi

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf "%b\n" "$ERRORS"
  echo "FAIL ($PASS passed, $FAIL failed)"
  exit 1
fi
echo "OK ($PASS checks)"
