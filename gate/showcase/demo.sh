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
# A program: no cookie jar; its tokens in a file of its own.
TOKENS=/jar/robot-tokens.json
export GATE_CACERT=/certs/public.crt GATE_CONNECT_TO=nginx:443
p() { curl -sk --connect-to "gate.localhost:$GATE_PORT:nginx:443" "$@"; }
pcode() { p -o /dev/null -w '%{http_code}' "$@"; }
login() {
  code -X POST "$B/auth/api/firstfactor" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"password\":\"$2\",\"keepMeLoggedIn\":false}"
}

if [ "${1:-}" = session ]; then
  echo "the session kept before"
  expect "still opens the page" "$(code "$B/")" 200
  echo "the program's refresh token kept before"
  expect "still gets an access token for robot" "$(p -H "Authorization: Bearer $(gate-token token --refresh --file "$TOKENS")" \
    "$B/api/whoami.txt" | sed -n 's/^principal=//p')" robot
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
expect "who is logged in: nobody" "$(code -H 'Remote-User: alice' "$B/api/v1/userinfo")" 401
# A body over nginx's 1 MB default (an attached photo): asking gate must not
# hold it to that, or auth_request answers 500.
head -c 2000000 /dev/zero > /tmp/big
expect "a 2 MB upload answers 401, not 500" "$(code -X POST --data-binary @/tmp/big "$B/api/upload")" 401

echo "a program, robot: a person lets it in once"
# The program (gate-token) prints an address and waits for a code. The
# person, in a browser of their own, logs in as robot, allows it, and is
# shown the code on /gate/code; pasted to the program, it is exchanged for
# tokens, the refresh token kept in a file.
pjar=/tmp/person
rm -f "$pjar" /tmp/said /tmp/code-in "$TOKENS" "$TOKENS.lock"
mkfifo /tmp/code-in
gate-token login --gate "$B" --file "$TOKENS" < /tmp/code-in 2> /tmp/said &
program=$!
exec 3> /tmp/code-in
for _ in $(seq 100); do grep -q "$B/auth/api/oidc/authorization" /tmp/said && break; sleep 0.1; done
url=$(grep -o "$B/auth/api/oidc/authorization[^ ]*" /tmp/said)
expect "the program prints an address at the gate" "$([ -n "$url" ] && echo ok || cat /tmp/said)" ok
pc() { curl -sk --connect-to "gate.localhost:$GATE_PORT:nginx:443" -b "$pjar" -c "$pjar" "$@"; }
to=$(pc -o /dev/null -w '%{redirect_url}' "$url")
case "$to" in "$B/auth/?"*flow_id=*) expect "  … opened, it asks to log in first" ok ok ;;
  *) expect "  … opened, it asks to log in first" "$to" "$B/auth/?…flow_id=…" ;; esac
expect "  … the person logs in as robot" "$(pc -o /dev/null -w '%{http_code}' -X POST "$B/auth/api/firstfactor" \
  -H 'Content-Type: application/json' -d '{"username":"robot","password":"robot-showcase"}')" 200
flow=$(pc -o /dev/null -w '%{redirect_url}' "$url" | sed -n 's/.*[?&]flow_id=\([^&]*\).*/\1/p')
expect "  … is asked to let it in" "$(pc "$B/auth/api/oidc/consent?flow_id=$flow" | grep -o '"scopes":\[[^]]*\]')" \
  '"scopes":["offline_access","authelia.bearer.authz"]'
back=$(pc -X POST "$B/auth/api/oidc/consent" -H 'Content-Type: application/json' \
  -d "{\"flow_id\":\"$flow\",\"client_id\":\"programs\",\"consent\":true,\"pre_configure\":false}" \
  | sed -n 's/.*"redirect_uri":"\([^"]*\)".*/\1/p' | sed 's/\\u0026/\&/g')
# Authelia answers with a form the browser posts on by itself.
pc "$back" > /tmp/form
expect "  … allows it, and the browser posts to" "$(sed -n 's/.*<form method="post" action="\([^"]*\)".*/\1/p' /tmp/form)" "$B/gate/code"
# What it posts: the code, the issuer, the state (no spaces in any).
set -- $(sed -n 's/.*<input type="hidden" name="\(code\|iss\|state\)" value="\([^"]*\)".*/--data-urlencode \1=\2/p' /tmp/form)
expect "  … that page, without a session: login" "$(pcode -X POST "$@" "$B/gate/code")" 302
shown=$(pc -X POST "$@" "$B/gate/code" | sed -n 's/.*<code id="code">\([^<]*\)<.*/\1/p')
expect "  … with it, shows the code" "$(echo "$shown" | grep -o '^authelia_ac_')" authelia_ac_
echo "$shown" >&3
exec 3>&-
wait $program
expect "the program keeps a refresh token, 0600" \
  "$(grep -c '"refresh_token": "authelia_rt_' "$TOKENS") $(stat -c %a "$TOKENS")" "1 600"

echo "the program on the API with an access token"
token=$(gate-token token --file "$TOKENS")
seen=$(p -H "Authorization: Bearer $token" -H 'X-Principal-Id: mallory' "$B/api/whoami.txt")
expect "X-Principal-Id (client sent mallory)" "$(echo "$seen" | sed -n 's/^principal=//p')" robot
expect "Authorization" "[$(echo "$seen" | sed -n 's/^authorization=//p')]" "[]"
expect "a wrong token on an API" "$(pcode -H "Authorization: Bearer ${token}x" "$B/api/whoami.txt")" 401
expect "a page does not open, to the login page" "$(pcode -H "Authorization: Bearer $token" "$B/")" 302
expect "who is logged in: nobody" "$(pcode -H "Authorization: Bearer $token" "$B/api/v1/userinfo")" 401
expect "its password as HTTP Basic: not taken" "$(pcode -u robot:robot-showcase "$B/api/whoami.txt")" 401
echo "  … the access token ends (10 s in the showcase)"
kept=$(sed -n 's/.*"refresh_token": "\([^"]*\)".*/\1/p' "$TOKENS")
sleep 11
expect "the old one on the API" "$(pcode -H "Authorization: Bearer $token" "$B/api/whoami.txt")" 401
fresh=$(gate-token token --file "$TOKENS")
expect "gate-token refreshes it, no person" "$([ -n "$fresh" ] && [ "$fresh" != "$token" ] && echo new)" new
expect "  … and the API answers robot" "$(p -H "Authorization: Bearer $fresh" "$B/api/whoami.txt" | sed -n 's/^principal=//p')" robot
expect "  … and keeps the new refresh token" "$([ "$(sed -n 's/.*"refresh_token": "\([^"]*\)".*/\1/p' "$TOKENS")" != "$kept" ] && echo new)" new
expect "a password as Basic opens no page" "$(pcode -u alice:alice-showcase "$B/")" 302

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
expect "a 2 MB upload reaches the application" "$(code -X POST --data-binary @/tmp/big "$B/api/upload")" 204

echo "who is logged in, for the person's page"
expect "userinfo" "$(c -H 'Remote-User: mallory' "$B/api/v1/userinfo")" \
  '{"sub":"alice","preferred_username":"alice","name":"Alice","email":"alice@example.com","end_session_endpoint":"/auth/logout"}'

echo "mail"
# Registering a passkey starts with a one-time code mailed to the account.
c -o /dev/null -X POST "$B/auth/api/user/session/elevation"
expect "a one-time code mailed to alice" "$(c "$MAIL/search?query=to:alice@example.com" | grep -o '"Subject":"[^"]*Confirm your identity"' | head -1 | cut -d'"' -f4)" "[Authelia] Confirm your identity"

logout() { c -o /dev/null -X POST "$B/auth/api/logout" -H 'Content-Type: application/json' -d '{}'; }

echo "log out"
logout
expect "the API is closed again" "$(code "$B/api/whoami.txt")" 401
expect "userinfo too" "$(code "$B/api/v1/userinfo")" 401

echo "another person"
expect "bob" "$(login bob bob-showcase)" 200
expect "the page" "$(c "$B/" | grep -o 'hello, bob')" "hello, bob"
expect "X-Principal-Id" "$(c "$B/api/whoami.txt" | sed -n 's/^principal=//p')" bob
expect "alice's password is not bob's" "$(logout; login bob alice-showcase)" 401

login alice alice-showcase >/dev/null

exit $FAILED
