#!/bin/sh
set -u
S=http://subjects:8083
step() { printf '\n== %s\n' "$1"; }
post() { curl -s -w ' %{http_code}\n' -X POST $S/subjects -H 'Content-Type: application/json' -d "$1"; }

step "create alice, with preferences only her readers understand"
post '{"name":"Alice","nick":"alice","subject_type":"human","preferences":{"timezone":"Europe/Sofia","x-own":{"any":[1,"thing"]}}}'

step "the same nick again"
post '{"name":"Another Alice","nick":"alice","subject_type":"human"}'

step "a nick that breaks the rules"
post '{"name":"Bob","nick":"Bob!","subject_type":"human"}'

step "preferences that are not an object"
post '{"name":"Bob","nick":"bob","subject_type":"ai","preferences":["no"]}'

step "everyone"
curl -s -w '\n' $S/subjects

ID=$(curl -s $S/subjects | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
step "rename alice; the nick cannot change"
curl -s -w ' %{http_code}\n' -X PUT $S/subjects/$ID -d '{"name":"Alice","nick":"alicia","subject_type":"human"}'

step "delete alice, then read her"
curl -s -w ' %{http_code}\n' -X DELETE $S/subjects/$ID
curl -s -o /dev/null -w '%{http_code}\n' $S/subjects/$ID

step "her nick stays taken"
post '{"name":"New Alice","nick":"alice","subject_type":"human"}'
