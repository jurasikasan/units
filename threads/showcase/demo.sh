#!/bin/sh
set -u
T=http://threads:8085
ALICE=00000000-0000-0000-0000-0000000a11ce
BOB=00000000-0000-0000-0000-000000000b0b
step() { printf '\n== %s\n' "$1"; }
post() { curl -s -w ' %{http_code}\n' -X POST "$1" -H 'Content-Type: application/json' -d "$2"; }
idof() { sed -n 's/^{"id":"\([^"]*\)".*/\1/p'; }

step "alice opens a thread"
TH=$(curl -s -X POST $T/threads -d "{\"opened_by\":\"$ALICE\"}" | idof)
echo "$TH"

step "alice and bob join, alice names it"
post $T/threads/$TH/entries "{\"sender\":\"$ALICE\",\"kind\":\"join\"}"
post $T/threads/$TH/entries "{\"sender\":\"$BOB\",\"kind\":\"join\"}"
post $T/threads/$TH/entries "{\"sender\":\"$ALICE\",\"kind\":\"update\",\"name\":\"Kitchen\"}"

step "alice says something; bob looks it up with a tool and answers"
post $T/threads/$TH/entries "{\"sender\":\"$ALICE\",\"kind\":\"create\",\"content\":[{\"type\":\"text\",\"text\":\"Is it raining?\"}]}"
post $T/threads/$TH/entries "{\"sender\":\"$BOB\",\"kind\":\"create\",\"content\":[{\"type\":\"tool_call\",\"id\":\"c1\",\"name\":\"weather\",\"args\":{\"city\":\"Sofia\"}}]}"
post $T/threads/$TH/entries "{\"sender\":\"$BOB\",\"kind\":\"create\",\"content\":[{\"type\":\"tool_result\",\"call_id\":\"c1\",\"text\":\"light rain\"}]}"
post $T/threads/$TH/entries "{\"sender\":\"$BOB\",\"kind\":\"create\",\"content\":[{\"type\":\"text\",\"text\":\"Yes, a little.\"}]}"

step "refused: a role, a join that says something, a kind we do not use"
post $T/threads/$TH/entries "{\"sender\":\"$ALICE\",\"kind\":\"create\",\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"x\"}]}"
post $T/threads/$TH/entries "{\"sender\":\"$ALICE\",\"kind\":\"join\",\"content\":[{\"type\":\"text\",\"text\":\"x\"}]}"
post $T/threads/$TH/entries "{\"sender\":\"$ALICE\",\"kind\":\"like\"}"

step "bob leaves; the thread, its name and members"
post $T/threads/$TH/entries "{\"sender\":\"$BOB\",\"kind\":\"leave\"}"
curl -s -w '\n' $T/threads/$TH

step "the last three entries, oldest first"
curl -s -w '\n' "$T/threads/$TH/entries?tail=true&limit=3"

step "bob's work under it: a child thread; then alice's threads"
post $T/threads "{\"parent\":\"$TH\",\"opened_by\":\"$BOB\"}"
curl -s -w '\n' "$T/threads?parent=$TH"
curl -s -w '\n' "$T/threads?member=$ALICE"

step "delete the thread: its child goes too"
curl -s -o /dev/null -w '%{http_code}\n' -X DELETE $T/threads/$TH
curl -s -w '\n' "$T/threads"
