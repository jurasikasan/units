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
run "unit"  python3 ../../scripts/check_unit.py .

if command -v golangci-lint &>/dev/null; then
  run "lint" golangci-lint run ./...
fi

# ffmpeg decodes the video (CONTRACT §4.1). Tests take it from PATH, as the
# image has it from its Dockerfile. Where it (or its libx264, which the
# sending side of the tests uses) is not, the tests through real ffmpeg skip;
# the RTP, timing, sampling and session logic is tested without it, through a
# fake decoder. Say which it was, so a skip is never silent.
if ! command -v ffmpeg &>/dev/null; then
  echo "  - ffmpeg: not on PATH; the tests through real ffmpeg are skipped (apt install ffmpeg to run them)"
elif [[ "$(ffmpeg -hide_banner -encoders 2>/dev/null)" != *libx264* ]]; then
  echo "  - ffmpeg: on PATH without libx264; the RTP-through-ffmpeg test is skipped"
else
  echo "  - ffmpeg: on PATH with libx264; the tests through real ffmpeg run"
fi

# No -maxdepth: a test nested deeper than a limit would be skipped silently.
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
