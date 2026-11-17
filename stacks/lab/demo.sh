#!/bin/sh
# demo.sh <user> <password> [mail] — that person logs in through the gate and
# is greeted by name; with `mail`, a one-time code reaches them in Mailpit.
set -u
USER_NAME=$1 PASSWORD=$2
B=$LAB_URL
hostport=${LAB_URL#https://}
case "$hostport" in *:*) ;; *) hostport=$hostport:443 ;; esac
JAR=/tmp/jar
FAILED=0

c() { curl -sk --connect-to "$hostport:nginx:443" -b "$JAR" -c "$JAR" "$@"; }
code() { c -o /dev/null -w '%{http_code}' "$@"; }
expect() {
  if [ "$2" = "$3" ]; then printf '  %-36s %s\n' "$1" "$2"
  else printf '  %-36s got %s, want %s\n' "$1" "$2" "$3"; FAILED=1; fi
}

expect "no session: the login page" "$(code "$B/")" 302
expect "$USER_NAME logs in" "$(code -X POST "$B/auth/api/firstfactor" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USER_NAME\",\"password\":\"$PASSWORD\"}")" 200
expect "the page" "$(c "$B/" | grep -o "hello, $USER_NAME")" "hello, $USER_NAME"
expect "X-Principal-Id" "$(c "$B/api/whoami.txt" | sed -n 's/^principal=//p')" "$USER_NAME"

if [ "${3:-}" = mail ]; then
  c -o /dev/null -X POST "$B/auth/api/user/session/elevation"
  expect "a one-time code by mail" "$(curl -s 'http://mailpit:8025/api/v1/messages' | grep -o 'Confirm your identity' | head -1)" "Confirm your identity"
fi
exit $FAILED
