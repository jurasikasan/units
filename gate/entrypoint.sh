#!/bin/sh
# The secrets are made on the first start and kept in /secrets; they are state,
# like /data, and a restore needs both.
set -e
mkdir -p /secrets /data
for name in session storage reset-jwt; do
  [ -s "/secrets/$name" ] || authelia crypto rand --length 64 --charset alphanumeric --file "/secrets/$name" >/dev/null
done
# The OpenID Connect provider's signing key and HMAC secret, for programs' tokens.
[ -s /secrets/oidc-hmac ] || authelia crypto rand --length 64 --charset alphanumeric --file /secrets/oidc-hmac >/dev/null
[ -s /secrets/oidc-jwk.pem ] || authelia crypto pair rsa generate --bits 2048 --directory /secrets --file.private-key oidc-jwk.pem --file.public-key oidc-jwk.pub.pem >/dev/null

# Authelia does not start without an account; say how to make the first one.
if { [ -z "${1:-}" ] || [ "$1" = "--config" ]; } && [ ! -s /data/users.yml ]; then
  echo "gate: no accounts yet; add the first one: gate-user add <name> <email> < password" >&2
  exit 1
fi
exec /app/entrypoint.sh "$@"
