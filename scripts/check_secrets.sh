#!/usr/bin/env bash
# Secrets must not reach this public repository. Two checks, over every
# commit (all branches) and the index:
#
#   1. gitleaks with .gitleaks.toml — its defaults plus passwords in URLs and
#      secret-looking assignments, which the defaults miss.
#   2. live values — every value in the gitignored env files on this machine
#      (stacks/*/.env, _ignored/*.env*, or the files given as arguments) is
#      looked for literally. Patterns guess; this knows.
#
# A hit names the file, line and rule (or the variable's name), never the
# value. Exit 1 on any hit. Run from anywhere in the repo; scripts/hooks/pre-push
# runs it before every push.
set -euo pipefail
set +x

root=$(git rev-parse --show-toplevel)
cd "$root"
fail=0

# --- 1. gitleaks -------------------------------------------------------------
GITLEAKS_IMAGE=zricethezav/gitleaks:v8.21.2
report=$(mktemp)
trap 'rm -f "$report"' EXIT

run_gitleaks() { # mode args...
    if command -v gitleaks >/dev/null 2>&1; then
        gitleaks "$@" --config .gitleaks.toml --redact --no-banner --log-level error \
            -f json -r "$report" --exit-code 0
    else
        docker run --rm -u "$(id -u):$(id -g)" -v "$root":/repo -v "$report":/report.json \
            -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0='*' \
            "$GITLEAKS_IMAGE" "$1" "${2/#./\/repo}" "${@:3}" \
            --config /repo/.gitleaks.toml --redact --no-banner --log-level error \
            -f json -r /report.json --exit-code 0
    fi
}

summarise() { # what
    python3 - "$report" "$1" <<'PY'
import json, sys
path, what = sys.argv[1], sys.argv[2]
try:
    hits = json.load(open(path)) or []
except (OSError, ValueError):
    print(f"secrets: gitleaks ({what}) wrote no report", file=sys.stderr)
    sys.exit(2)
for h in hits:
    where = f"{h['File']}:{h['StartLine']}"
    commit = f" in {h['Commit'][:8]}" if h.get("Commit") else ""
    print(f"secrets: {h['RuleID']}: {where}{commit}")
sys.exit(1 if hits else 0)
PY
}

run_gitleaks git . --log-opts=--all
summarise "history" || fail=1
run_gitleaks git . --staged
summarise "index" || fail=1

# --- 2. live values ----------------------------------------------------------
if [ $# -gt 0 ]; then
    env_files=("$@")
else
    shopt -s nullglob
    env_files=(stacks/*/.env _ignored/*.env*)
    shopt -u nullglob
fi

# A value that is the same line as in a tracked .env.example is a published
# default, not a secret.
published=$(git ls-files -z -- '*.env.example' | xargs -0 -r cat | grep -E '^[A-Za-z_][A-Za-z0-9_]*=' || true)

checked=0
for f in "${env_files[@]}"; do
    [ -f "$f" ] || continue
    while IFS= read -r line; do
        case "$line" in ''|'#'*) continue ;; esac
        [[ "$line" == *=* ]] || continue
        grep -qxF -e "${line#export }" <<<"$published" && continue
        name=${line%%=*}
        name=${name#export }
        value=${line#*=}
        value=${value%\"}; value=${value#\"}
        value=${value%\'}; value=${value#\'}
        # Short or structural values (ports, hostnames, flags) are not secrets
        # and would match everywhere.
        [ ${#value} -ge 12 ] || continue
        [[ "$value" =~ ^[0-9.:]+$ ]] && continue
        checked=$((checked + 1))
        if git grep -q --cached -F -e "$value" 2>/dev/null; then
            echo "secrets: live value of $name ($f) is in the index"
            fail=1
        fi
        if [ -n "$(git log --all --format=%h -S"$value" 2>/dev/null | head -1)" ]; then
            echo "secrets: live value of $name ($f) is in history"
            fail=1
        fi
    done < "$f"
done

if [ "$fail" -ne 0 ]; then
    echo "secrets: FAILED — nothing above may be pushed" >&2
    exit 1
fi
echo "secrets: ok (gitleaks over history and index, $checked live values from ${#env_files[@]} env files)"
