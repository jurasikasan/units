#!/bin/sh
set -u
S=http://artifact_store:8094
step() { printf '\n== %s\n' "$1"; }
code() { curl -s -o /dev/null -w '%{http_code}\n' "$@"; }

step "put a file, read it back"
printf hello | curl -s -w ' %{http_code}\n' -X PUT --data-binary @- -H 'Content-Type: text/plain' $S/v1/docs/R-1.a
curl -s -w '\n' $S/v1/docs/R-1.a

step "put again under the same name: it replaces"
printf world | curl -s -w ' %{http_code}\n' -X PUT --data-binary @- -H 'Content-Type: text/plain' $S/v1/docs/R-1.a
curl -s -w '\n' $S/v1/docs/R-1.a

step "the same bytes under another name: one object, equal sha256"
printf world | curl -s -w ' %{http_code}\n' -X PUT --data-binary @- -H 'Content-Type: text/plain' $S/v1/docs/R-2.b

step "list the space; then only R-1's files"
curl -s -w '\n' $S/v1/docs
curl -s -w '\n' "$S/v1/docs?prefix=R-1."

step "refused: a missing file (404), a space out of its alphabet (400)"
code $S/v1/docs/no.such
printf x | code -X PUT --data-binary @- $S/v1/Docs/x

step "delete both; the space is empty"
code -X DELETE $S/v1/docs/R-1.a
code -X DELETE $S/v1/docs/R-2.b
curl -s -w '\n' $S/v1/docs
