#!/bin/bash
set -euo pipefail

# The same shape as the Go modules' check.sh: an executor's only report is this
# output, and a second format to read is a second thing to get wrong.
#
# Standard library only, on purpose. The executor's image (.devcontainer) has
# python3 and nothing else — no pytest, no fastapi, no torch — and a gate that
# needs a pip install there needs the network too.

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

# Every .py in the module, not only server.py: a helper split out for testing
# is exactly the file that would otherwise go unchecked. ast.parse, not
# py_compile: py_compile writes __pycache__ even under -B, and that lands in
# the diff an executor publishes.
compile_all() {
  find . -name "*.py" -not -path "*/__pycache__/*" -print0 \
    | xargs -0 -r python3 -c 'import ast, sys
for f in sys.argv[1:]:
    ast.parse(open(f, encoding="utf-8").read(), f)'
}

run "compile" compile_all

# Every local module the service imports is in the image: the tests
# below run from the source tree, where every file is present, so a module
# split out of server.py and missing from the Dockerfile's COPY passes them and
# kills the container on its first import (loader.py, voices.py).
run "dockerfile" python3 ../../check_dockerfile_copies.py .

# unit.yaml against the code: env, the HTTP door.
run "unit" python3 ../../../tools/check_unit.py .

# unittest, discovered from test_*.py anywhere in the module. A test that
# imports server.py imports its runtime dependencies too and fails here — keep
# the logic under test in a file that imports only the standard library.
# $(…) rather than `find | grep -q`: under pipefail an early-exiting grep can
# SIGPIPE find and turn "tests exist" into "skip the tests".
if [ -n "$(find . -name "test_*.py" -not -path "*/__pycache__/*")" ]; then
  run "test" env PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s . -p "test_*.py"
fi

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf "%b\n" "$ERRORS"
  echo "FAIL ($PASS passed, $FAIL failed)"
  exit 1
fi
echo "OK ($PASS checks)"

# What this gate does NOT prove: that the service starts. Imports of fastapi,
# torch or a model are not resolved here, a route can be wired to nothing, and
# with no test_*.py the only check is that the files parse. Green means the
# Python is valid and the tests that exist pass — nothing about the container.
