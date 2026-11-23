# throwaway.sh <name> — sourced, from stacks/lab: a lab of its own from
# .env.example on free ports, never the one .env runs. Its compose project
# and its images are <name>-<pid>, so another checkout's check (or the
# nightly) running at the same time builds units-*:<its tag>, never these;
# everything it made goes when the shell exits. check.sh and
# nightly/e2e.sh stand on it.
#
# Gives: run <label> <cmd…> (a line ✓/✗ each, the output of a failed one
# kept), e2e <scenario> <label> [VAR=value…] (one of e2e/chat.mjs's
# scenarios as the first account), finish (the tally, exit 1 on any
# failure), $work (a scratch directory), $first_user, $first_password, and
# $FIRST_FAIL: the first failed line and its first FAIL, for whoever tells
# a person.

PASS=0
FAIL=0
ERRORS=""
FIRST_FAIL=""

run() {
  local label="$1"; shift
  if output=$("$@" 2>&1); then
    echo "  ✓ $label"
    PASS=$((PASS + 1))
  else
    echo "  ✗ $label"
    ERRORS="$ERRORS\n--- $label ---\n$output"
    FAIL=$((FAIL + 1))
    if [ -z "$FIRST_FAIL" ]; then
      FIRST_FAIL="$label — $(echo "$output" | grep -m1 -E 'FAIL|got .*, want' || echo "$output" | tail -1)"
    fi
  fi
}

free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }
work=$(mktemp -d)
port=$(free_port)
sed -e "s|^LAB_URL=.*|LAB_URL=https://lab.localhost:$port|" \
    -e "s|^LAB_PORT=.*|LAB_PORT=$port|" \
    -e "s|^LAB_MAILPIT_PORT=.*|LAB_MAILPIT_PORT=$(free_port)|" \
    -e "s|^COMPOSE_PROFILES=.*|COMPOSE_PROFILES=mailpit,everything|" \
    -e "s|^LAB_MCP_SERVERS=\(.*\)|LAB_MCP_SERVERS=\1,everything=http://everything:3001/mcp|" \
    -e "s|^LAB_HOLD_GRACE=.*|LAB_HOLD_GRACE=45s|" .env.example > "$work/env"
export COMPOSE_ENV_FILES="$work/env" COMPOSE_PROJECT_NAME="$1-$$" UNITS_TAG="$1-$$"
first_user=$(sed -n 's/^LAB_FIRST_USER=//p' .env.example)
first_password=$(sed -n 's/^LAB_FIRST_PASSWORD=//p' .env.example)
own_images() { docker images --filter "reference=units-*:$UNITS_TAG" --format '{{.Repository}}:{{.Tag}}'; }
trap 'docker compose --profile "*" down -v --remove-orphans >/dev/null 2>&1 || true; own_images | xargs -r docker image rm >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

# A browser's scenario (e2e/chat.mjs) by name, a run of its own, as the
# first account; what follows the label is set in its environment.
e2e() {
  local name=$1 label=$2; shift 2
  run "e2e $name: $label" env "$@" E2E_ONLY="$name" docker compose run --rm e2e "$first_user" "$first_password"
}

finish() {
  echo ""
  if [ "$FAIL" -gt 0 ]; then
    printf "%b\n" "$ERRORS"
    echo "FAIL ($PASS passed, $FAIL failed)"
    exit 1
  fi
  echo "OK ($PASS checks)"
}
