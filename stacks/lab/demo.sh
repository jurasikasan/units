#!/bin/sh
# demo.sh <user> <password> [mail] — that person logs in through the gate,
# steers their two subjects (<user> and <user>-assistant, made on the first
# visit) and the chat answers them; with `mail`, a one-time code reaches them
# in Mailpit.
set -u
USER_NAME=$1 PASSWORD=$2
B=$LAB_URL
hostport=${LAB_URL#https://}
case "$hostport" in *:*) ;; *) hostport=$hostport:443 ;; esac
JAR=/tmp/jar
FAILED=0
RUN='{"threadId":"demo","runId":"r1","messages":[{"id":"m1","role":"user","content":"Hi!"}]}'

c() { curl -sk --connect-to "$hostport:nginx:443" -b "$JAR" -c "$JAR" "$@"; }
code() { c -o /dev/null -w '%{http_code}' "$@"; }
expect() {
  if [ "$2" = "$3" ]; then printf '  %-36s %s\n' "$1" "$2"
  else printf '  %-36s got %s, want %s\n' "$1" "$2" "$3"; FAILED=1; fi
}
run() { c -N "$B/api/run" -H 'Content-Type: application/json' -d "$RUN"; }

expect "no session: the login page" "$(code "$B/")" 302
expect "no session: no subjects" "$(code "$B/api/reins/me/subjects")" 401
expect "no session: the chat is closed" "$(code "$B/api/run" -H 'Content-Type: application/json' -d "$RUN")" 401
expect "$USER_NAME logs in" "$(code -X POST "$B/auth/api/firstfactor" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USER_NAME\",\"password\":\"$PASSWORD\"}")" 200
mine=$(c "$B/api/reins/me/subjects")
nicks=$(echo "$mine" | grep -o '"nick":"[^"]*"' | sed 's/"nick":"\(.*\)"/\1/' | sort | tr '\n' ' ')
expect "$USER_NAME steers" "$nicks" "$USER_NAME $USER_NAME-assistant "
# The same subjects; who holds each may change between two reads.
ids() { grep -o '"id":"[^"]*","name"' | sort; }
expect "and the same ones again" "$(c "$B/api/reins/me/subjects" | ids | tr '\n' ' ')" "$(echo "$mine" | ids | tr '\n' ' ')"
expect "the page" "$(c "$B/" | grep -o '<title>Chat</title>')" "<title>Chat</title>"
out=$(run)
expect "the chat answers" "$(echo "$out" | grep -c '"type":"TEXT_MESSAGE_CONTENT"' | sed 's/^[1-9][0-9]*$/yes/')" yes
expect "the run finishes" "$(echo "$out" | grep -o '"type":"RUN_FINISHED"')" '"type":"RUN_FINISHED"'

if [ "${3:-}" = mail ]; then
  c -o /dev/null -X POST "$B/auth/api/user/session/elevation"
  expect "a one-time code by mail" "$(curl -s 'http://mailpit:8025/api/v1/messages' | grep -o 'Confirm your identity' | head -1)" "Confirm your identity"
fi
exit $FAILED
