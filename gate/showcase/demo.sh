#!/bin/sh
# Walks through the gate with curl, the way a browser and a program would.
#   demo.sh          the whole walk; it ends logged in, the session kept in /jar
#   demo.sh session  that session still opens the page (after gate restarts)
set -u
B="https://gate.localhost:$GATE_PORT"
JAR=/jar/cookies
MAIL=http://mailpit:8025/api/v1
FAILED=0

c() { curl -sk --connect-to "gate.localhost:$GATE_PORT:nginx:443" -b "$JAR" -c "$JAR" "$@"; }
code() { c -o /dev/null -w '%{http_code}' "$@"; }
expect() {
  if [ "$2" = "$3" ]; then printf '  %-42s %s\n' "$1" "$2"
  else printf '  %-42s got %s, want %s\n' "$1" "$2" "$3"; FAILED=1; fi
}
login() {
  code -X POST "$B/auth/api/firstfactor" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"password\":\"$2\",\"keepMeLoggedIn\":false}"
}

if [ "${1:-}" = session ]; then
  echo "the session kept before"
  expect "still opens the page" "$(code "$B/")" 200
  exit $FAILED
fi

rm -f "$JAR"
c -o /dev/null -X DELETE "$MAIL/messages"

echo "no session"
expect "a page sends to the login page" "$(code "$B/")" 302
loc=$(c -o /dev/null -w '%{redirect_url}' "$B/")
case "$loc" in "$B/auth/?rd="*) expect "  … at /auth/?rd=" ok ok ;; *) expect "  … at /auth/?rd=" "$loc" "$B/auth/?rd=…" ;; esac
expect "an API answers 401" "$(code "$B/api/whoami.txt")" 401
expect "a forged X-Principal-Id changes nothing" "$(code -H 'X-Principal-Id: alice' "$B/api/whoami.txt")" 401

echo "log in"
expect "wrong password" "$(login alice wrong)" 401
expect "alice" "$(login alice alice-showcase)" 200
expect "  … sent on to the application" "$(c -X POST "$B/auth/api/firstfactor" -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"alice-showcase"}' | sed -n 's/.*"redirect":"\([^"]*\)".*/\1/p')" "$B/"

echo "what the application sees"
expect "the page" "$(c "$B/" | grep -o 'hello, alice')" "hello, alice"
seen=$(c -H 'X-Principal-Id: mallory' -H 'Authorization: Bearer stolen' "$B/api/whoami.txt")
expect "X-Principal-Id (client sent mallory)" "$(echo "$seen" | sed -n 's/^principal=//p')" alice
expect "Cookie" "[$(echo "$seen" | sed -n 's/^cookie=//p')]" "[]"
expect "Authorization (client sent a Bearer)" "[$(echo "$seen" | sed -n 's/^authorization=//p')]" "[]"

echo "mail"
# Registering a passkey starts with a one-time code mailed to the account.
c -o /dev/null -X POST "$B/auth/api/user/session/elevation"
expect "a one-time code mailed to alice" "$(c "$MAIL/search?query=to:alice@example.com" | grep -o '"Subject":"[^"]*Confirm your identity"' | head -1 | cut -d'"' -f4)" "[Authelia] Confirm your identity"

logout() { c -o /dev/null -X POST "$B/auth/api/logout" -H 'Content-Type: application/json' -d '{}'; }

echo "log out"
logout
expect "the API is closed again" "$(code "$B/api/whoami.txt")" 401

echo "another person"
expect "bob" "$(login bob bob-showcase)" 200
expect "the page" "$(c "$B/" | grep -o 'hello, bob')" "hello, bob"
expect "X-Principal-Id" "$(c "$B/api/whoami.txt" | sed -n 's/^principal=//p')" bob
expect "alice's password is not bob's" "$(logout; login bob alice-showcase)" 401

login alice alice-showcase >/dev/null

exit $FAILED
