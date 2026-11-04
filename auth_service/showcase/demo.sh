#!/bin/sh
set -u
G=http://gate
J=/tmp/cookies
step() { printf '\n== %s\n' "$1"; }

step "private page without a session"
curl -s -o /dev/null -w '%{http_code}\n' $G/private/

step "login with a wrong password"
curl -s -w ' %{http_code}\n' -X POST $G/auth/login -d '{"username":"alice","password":"not-the-password"}'

step "login as alice"
curl -s -c $J -w ' %{http_code}\n' -X POST $G/auth/login -d '{"username":"alice","password":"correct horse battery"}'

step "private page with the session cookie"
curl -s -b $J $G/private/

step "who am I"
curl -s -b $J -w '\n' $G/auth/me

step "logout, then the private page again"
curl -s -b $J -c $J -o /dev/null -X POST $G/auth/logout
curl -s -b $J -o /dev/null -w '%{http_code}\n' $G/private/
