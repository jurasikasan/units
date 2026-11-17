# gate

The door in front of applications. A person logs in on a page at `/auth`;
nginx asks before serving anything protected whether the request carries a
session; the application behind gets one header, `X-Principal-Id`, and never
sees a password, a cookie or a token.

None of it is our code. Accounts, sessions, the login page and passkeys are
[Authelia](https://www.authelia.com) (Apache-2.0); the asking is nginx
`auth_request`. The unit is the configuration that makes the two one
contract: an image with Authelia's configuration rendered from the
environment, and nginx snippets a site includes.

## Showcase

```bash
cd showcase
docker compose up -d --wait
docker compose run --rm demo
```

Authelia with Redis for sessions, Mailpit catching its mail, nginx with a
self-signed certificate, and a page behind it that says hello to whoever
passed. A one-shot `seed` makes the accounts, alice and bob. The demo walks
through with curl:

```
no session
  a page sends to the login page             302
  an API answers 401                         401
  a forged X-Principal-Id changes nothing    401
log in
  wrong password                             401
  alice                                      200
    … sent on to the application           https://gate.localhost:8443/
what the application sees
  the page                                   hello, alice
  X-Principal-Id (client sent mallory)       alice
  Cookie                                     []
  Authorization (client sent a Bearer)       []
mail
  a one-time code mailed to alice            [Authelia] Confirm your identity
log out
  the API is closed again                    401
another person
  bob                                        200
  the page                                   hello, bob
  X-Principal-Id                             bob
  alice's password is not bob's              401
```

It ends logged in again; `docker compose restart gate` and then
`docker compose run --rm demo session` show the session outlives the restart.

In a browser: https://gate.localhost:8443/, `alice` / `alice-showcase` or
`bob` / `bob-showcase` (showcase passwords, in `showcase/compose.yaml`). The certificate is
self-signed, so the browser asks first. Mail lands in Mailpit,
http://localhost:8025. `GATE_PORT` and `MAILPIT_PORT` move the ports.

`./check.sh` validates the configuration, holds `unit.yaml`, and runs the
demo and the restart on free ports.

## The contract

- `X-Principal-Id` is the **username** of whoever passed. A value a client
  sends under that name is replaced, always.
- Neither `Cookie` nor `Authorization` travels past the gate.
- A page without a session is sent to the login page and comes back after
  it; a login started on the login page itself ends in the application, at
  `GATE_URL/`. A program gets a plain 401.

## Putting it in front of an application

The site's nginx includes the snippets in `nginx/` (mounted at, say,
`/etc/nginx/gate/`) and reaches the unit as `gate:9091`:

```nginx
server {
    listen 443 ssl;
    include /etc/nginx/gate/authz.conf;          # once per server

    location /auth { include /etc/nginx/gate/portal.conf; }
    location /api/ { include /etc/nginx/gate/api.conf;  proxy_pass http://app; }
    location /     { include /etc/nginx/gate/page.conf; proxy_pass http://app; }
}
```

A location that includes `page.conf` or `api.conf` takes its
`proxy_set_header` lines from there; nginx does not merge them with the
server's.

## Configuration

| Variable | |
|---|---|
| `GATE_DOMAIN` | the session cookie's domain, e.g. `gate.localhost` |
| `GATE_URL` | the site's public https base, port included, e.g. `https://gate.localhost:8443`; the login page is at `GATE_URL/auth` |
| `GATE_REDIS_HOST` | Redis for sessions, port 6379 |
| `GATE_SMTP_ADDRESS` | where mail goes, e.g. `submission://smtp.example.com:587` |
| `GATE_SMTP_SENDER` | the From of every mail, e.g. `gate <gate@example.com>` |
| `GATE_SMTP_USERNAME`, `GATE_SMTP_PASSWORD` | the relay's login, when it asks for one |

Authelia refuses a login URL that is not https, so even a local run has a
certificate; it also refuses a mail server without STARTTLS. The showcase
gives Mailpit a self-signed certificate and tells Authelia, with its own
`AUTHELIA_NOTIFIER_SMTP_TLS_SKIP_VERIFY`, not to check it; a deployment
leaves that out.

## Accounts

Authelia does not start without one, so the first is made before the first
start, and more any time after; the password comes on stdin:

```bash
docker compose run --rm gate gate-user add alice alice@example.com "Alice" < password.txt
```

Authelia notices the file change on its own.

## State

| Path | What |
|---|---|
| `/data/users.yml` | accounts: username, display name, email, argon2 hash |
| `/data/db.sqlite3` | passkeys, second factors, failed-login records; the sensitive columns encrypted with the storage key |
| `/secrets/` | the session, storage and reset keys, made on the first start |

`/data` and `/secrets` are backed up and restored together: Authelia refuses
to start on a database made with another storage key. Sessions are in Redis
and need no backup: losing them logs everyone out, nothing more.
