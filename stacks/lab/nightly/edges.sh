#!/bin/bash
# edges.sh — are our edge libraries still current? Brings them to latest in
# this checkout, which must be a throwaway one (run.sh makes it and sets
# NIGHTLY_THROWAWAY), then runs the touched units' check.sh, and writes a
# report for whoever adopts the news (Claude, handed it):
#
# - faces/web_chat: the npm packages matching EDGES_NPM (assistant-ui's,
#   AG-UI's) to their latest, the shadcn CLI upstream.sh pins to its
#   latest, then ./upstream.sh (src/upstream again from assistant-ui's
#   registry, our patches on top: one that no longer applies is named);
# - each go.mod requiring a module of EDGES_GO (the AG-UI Go SDK, untagged:
#   its main; openai-go: its latest), `go get` and `go mod tidy`;
# - per library, old → new and where to read what changed in between
#   (the changelog, or the commits between two refs); the diffstat of what
#   moved; the patches that fail; the checks that went red, with their first
#   error lines.
#
# NIGHTLY_REPORT gets the report (stdout without it). A line goes to
# NIGHTLY_SUMMARY when something is new or red and differs from the last
# run's (NIGHTLY_STATE/edges.last): the same news is told once. Exit 1 when
# something is red (a check, a patch, a step that could not run), told now
# or before.
set -uo pipefail
cd "$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
[ "${NIGHTLY_THROWAWAY:-}" = yes ] || { echo "edges.sh changes the checkout it runs in: run it through nightly/run.sh edges, which makes a throwaway one" >&2; exit 2; }

EDGES_NPM=${EDGES_NPM:-'^@(assistant-ui|ag-ui)/'}
EDGES_GO=${EDGES_GO:-'github.com/ag-ui-protocol/ag-ui/sdks/community/go@main github.com/openai/openai-go/v3@latest'}
report=${NIGHTLY_REPORT:-/dev/stdout}
state=${NIGHTLY_STATE:-}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export HUSKY=0 npm_config_audit=false npm_config_fund=false

updated=()   # "name old → new"
links=()     # "name: url"
red=()       # "what: first lines"
note() { red+=("$1"); }

# A GitHub module path's repository, for links: github.com/o/r/… → https://github.com/o/r.
gh_repo() { echo "https://$(echo "$1" | cut -d/ -f1-3)"; }
# A Go version's ref on GitHub: a tag as is, a pseudo-version's commit.
go_ref() { case "$1" in *-*-*) echo "${1##*-}" ;; *) echo "$1" ;; esac; }

# --- faces/web_chat: npm and the registry ---
web=faces/web_chat
if (cd "$web" && npm ci --no-audit --no-fund >"$work/npm-ci" 2>&1); then
  mapfile -t pkgs < <(node -e '
    const p = require("./'"$web"'/package.json"); const re = new RegExp(process.argv[1])
    for (const n of Object.keys({ ...p.dependencies, ...p.devDependencies })) if (re.test(n)) console.log(n)' "$EDGES_NPM")
  want=()
  for p in "${pkgs[@]}"; do
    old=$(node -p "require('./$web/node_modules/$p/package.json').version")
    new=$(npm view "$p" version 2>/dev/null) || { note "npm view $p: no answer"; continue; }
    [ "$old" = "$new" ] && continue
    updated+=("$p $old → $new")
    want+=("$p@$new")
    repo=$(npm view "$p" repository.url 2>/dev/null | sed -E 's#^git\+##; s#\.git$##; s#^git://#https://#')
    dir=$(npm view "$p" repository.directory 2>/dev/null)
    if [ -n "$dir" ]; then links+=("$p: $repo/blob/main/$dir/CHANGELOG.md"); else links+=("$p: $repo/releases"); fi
  done
  if [ ${#want[@]} -gt 0 ]; then
    (cd "$web" && npm install --no-audit --no-fund "${want[@]}" >"$work/npm-install" 2>&1) || note "npm install ${want[*]}: $(tail -3 "$work/npm-install" | tr '\n' ' ')"
  fi
else
  note "$web: npm ci: $(tail -3 "$work/npm-ci" | tr '\n' ' ')"
fi
old=$(sed -n 's/^SHADCN=shadcn@//p' "$web/upstream.sh")
new=$(npm view shadcn version 2>/dev/null) || note "npm view shadcn: no answer"
if [ -n "$new" ] && [ "$old" != "$new" ]; then
  sed -i "s/^SHADCN=shadcn@.*/SHADCN=shadcn@$new/" "$web/upstream.sh"
  updated+=("shadcn (upstream.sh) $old → $new")
  links+=("shadcn: https://github.com/shadcn-ui/ui/blob/main/packages/shadcn/CHANGELOG.md")
fi
if ! "$web/upstream.sh" >"$work/upstream" 2>&1; then
  note "$web/upstream.sh: $(grep -m3 -E 'does not apply|rror' "$work/upstream" | tr '\n' ' ')"
fi
patches=$(grep 'does not apply' "$work/upstream" || true)

# --- Go: the modules in EDGES_GO, wherever required ---
for spec in $EDGES_GO; do
  mod=${spec%@*} query=${spec#*@}
  for gomod in $(grep -l -E "^\s*(require\s+)?$mod v" --include=go.mod --exclude-dir=node_modules -r . | sort); do
    dir=$(dirname "$gomod")
    old=$(cd "$dir" && go list -m -f '{{.Version}}' "$mod")
    if ! (cd "$dir" && go get "$mod@$query" >"$work/go-get" 2>&1 && go mod tidy >>"$work/go-get" 2>&1); then
      note "${dir#./}: go get $mod@$query: $(tail -2 "$work/go-get" | tr '\n' ' ')"
      continue
    fi
    new=$(cd "$dir" && go list -m -f '{{.Version}}' "$mod")
    [ "$old" = "$new" ] && continue
    updated+=("$mod in ${dir#./} $old → $new")
    links+=("$mod: $(gh_repo "$mod")/compare/$(go_ref "$old")...$(go_ref "$new")")
  done
done

# --- the touched units' checks ---
mapfile -t touched < <(git status --porcelain | cut -c4- | while read -r f; do
  d=$(dirname "$f"); while [ "$d" != . ] && [ ! -f "$d/check.sh" ]; do d=$(dirname "$d"); done
  [ "$d" != . ] && echo "$d"; done | sort -u)
checked=()
for u in "${touched[@]}"; do
  if (cd "$u" && bash check.sh >"$work/check" 2>&1); then
    checked+=("$u ✓")
  else
    checked+=("$u ✗")
    note "$u/check.sh: $(grep -m1 -E '^  ✗' "$work/check" | sed 's/^ *//')$(awk '/^--- /{n++} n==1 && !/^--- /' "$work/check" | grep -v '^\s*$' | head -5 | sed 's/^/\n      /' | tr -d '\r')"
  fi
done

# --- the report ---
diffstat=$(git diff --stat -- . ':!*package-lock.json' ':!*go.sum' | tail -25)
{
  echo "# edges, $(date -I), at $(git rev-parse --short HEAD)"
  echo
  echo "## updated"
  if [ ${#updated[@]} -eq 0 ]; then echo "nothing: every edge is at its latest"; else printf -- '- %s\n' "${updated[@]}"; fi
  [ ${#links[@]} -gt 0 ] && { echo; echo "## what changed in between"; printf -- '- %s\n' "${links[@]}" | sort -u; }
  echo
  echo "## moved (git diff --stat, lock files left out)"
  if [ -z "$diffstat" ]; then echo "nothing"; else echo '```'; echo "$diffstat"; echo '```'; fi
  echo
  echo "## patches that no longer apply"
  if [ -z "$patches" ]; then echo "none"; else echo "$patches" | sed 's/^/- /'; fi
  echo
  echo "## checks"
  if [ ${#checked[@]} -eq 0 ]; then echo "none run: nothing touched"; else printf -- '- %s\n' "${checked[@]}"; fi
  if [ ${#red[@]} -gt 0 ]; then echo; echo "## red"; printf -- '- %s\n' "${red[@]}"; fi
  echo
  echo "To adopt: in a units checkout, the same commands (faces/web_chat: npm install <pkg>@<new>, ./upstream.sh; a Go unit: go get <module>@<new>, go mod tidy), then the patches and the red checks above."
} > "$report"

# --- told once ---
moved=$( [ -n "$diffstat" ] && echo yes || echo no )
[ ${#updated[@]} -eq 0 ] && [ ${#red[@]} -eq 0 ] && [ "$moved" = no ] && exit 0
print=$( { printf '%s\n' "${updated[@]}" "${red[@]%%:*}"; git diff -- . ':!*package-lock.json' ':!*go.sum' | sha256sum; } | sha256sum | cut -c1-16)
last=""
[ -n "$state" ] && [ -f "$state/edges.last" ] && last=$(cat "$state/edges.last")
[ -n "$state" ] && echo "$print" > "$state/edges.last"
code=0
[ ${#red[@]} -gt 0 ] && code=1
if [ "$print" != "$last" ] && [ -n "${NIGHTLY_SUMMARY:-}" ]; then
  s="${#updated[@]} updated"
  [ ${#updated[@]} -gt 0 ] && s="$s ($(printf '%s, ' "${updated[@]}" | sed 's/, $//'))"
  [ ${#updated[@]} -eq 0 ] && [ "$moved" = yes ] && s="$s, src/upstream moved"
  [ -n "$patches" ] && s="$s; $(echo "$patches" | wc -l) patch(es) fail"
  [ ${#red[@]} -gt 0 ] && s="$s; red: $(printf '%s, ' "${red[@]%%:*}" | sed 's/, $//')"
  echo "edges: $s" > "$NIGHTLY_SUMMARY"
fi
exit $code
