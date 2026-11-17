#!/bin/bash
# src/upstream, again from assistant-ui's registry, with patches/ on top.
#
# src/upstream is generated and never edited by hand: it is what the
# standard chat client looks like today. After this, `git diff src/upstream
# package.json` is the news from the UI's edge; a patch that no longer
# applies means upstream changed where we had changed it. Our own code
# lives outside src/upstream (src/App.tsx, src/main.tsx).
set -euo pipefail
cd "$(dirname "$0")"

SHADCN=shadcn@4.23.0
THREAD=https://r.assistant-ui.com/thread.json

keep=$(mktemp)
log=$(mktemp)
cp components.json "$keep"
trap 'cp "$keep" components.json; rm -f "$keep" "$log"' EXIT
quiet() { "$@" </dev/null >"$log" 2>&1 || { cat "$log"; exit 1; }; }

rm -rf src/upstream src/components src/lib
mkdir -p src/upstream
printf '@import "tailwindcss";\n' > src/upstream/index.css

# init writes the theme into the one stylesheet it finds; the button and
# utils it also writes land on its default paths and are dropped, and add
# writes them again where components.json says.
quiet npx --yes "$SHADCN" init -y -f --no-reinstall -b base -p nova -t vite --no-monorepo --no-rtl
rm -rf src/components src/lib
cp "$keep" components.json
quiet npx --yes "$SHADCN" add -y utils button "$THREAD"

for p in patches/*.patch; do
  patch -p1 --forward --no-backup-if-mismatch -s < "$p" || { echo "$p: does not apply, upstream changed there"; exit 1; }
done
git diff --stat -- src/upstream package.json
