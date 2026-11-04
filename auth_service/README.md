# auth_service

A front door for applications that should not handle passwords. It answers
one question for nginx, *"is this request authenticated, and if so, who is
it?"*, and owns the only copy of the credentials needed to answer it.

nginx makes an `auth_request` subrequest to `/verify` before serving anything
protected, takes `X-Principal-Id` from the 200 and hands it to the application.
The application never sees a password, only a principal id.

**Requires** PostgreSQL (the showcase runs 16) and Redis, both its own:
nothing else should hold credentials to either.

## Showcase

```bash
cd showcase && docker compose up --build
```

Postgres, Redis, auth_service with a seeded account `alice`, an nginx gate in
front of a one-line "application", and a `demo` container that walks through it
with curl:

```
private page without a session          401
login with a wrong password             401 invalid credentials
login as alice                          200, Set-Cookie
private page with the session cookie    hello, principal <uuid>
who am I                                {"principal_id": …, "username": "alice"}
logout, then the private page again     401
```

The gate is `showcase/nginx.conf`: `/auth/` is public and goes to the service,
`/private/` sits behind `auth_request`. The gate is published on
`127.0.0.1:8080` for trying it from a browser or curl.

## HTTP (:8093)

- `POST /login`: `{"username","password"}` → `200 {"principal_id","username"}`
  and a session cookie. The body carries no token. `401 {"error":"invalid
  credentials"}` for every kind of failure (unknown user, wrong password,
  disabled account), `403` origin not allowed, `429` throttled.
- `POST /logout` → `204` always, with an expired cookie.
- `GET /verify`: the `auth_request` endpoint. `200` with `X-Principal-Id`, or
  `401`. Accepts the session cookie or `Authorization: Bearer <api token>`;
  the cookie is checked first, because it costs one Redis GET and no database.
- `GET /me` → `200 {"principal_id","username"}` or `401`.
- `POST /password`: change one's own password.
- `GET /health` → `200 {"status":"ok"}`.

The OpenAPI spec is in `docs/`, generated from the handlers' annotations by
`doc.sh`; `check.sh` fails when it is stale.

## State

Two stores of its own, which nothing else should be able to reach:

- **Postgres**: `accounts` and `api_tokens`, created at startup by one
  idempotent block (`internal/db/migrate.go`). There are no versioned
  migrations, so a column change has to be applied by hand as well.
  `accounts` holds only what proves who you are: name, recovery email,
  password hash, disabled flag. What describes you belongs to the application,
  keyed by the principal id.
- **Redis**: sessions and rate-limit counters.

Passwords are argon2id (64 MiB, t=1, p=4) in PHC format; the parameters travel
with each hash, so raising the cost later does not invalidate existing rows.
Sessions are opaque tokens, not JWT, so they can be revoked; only their
SHA-256 is stored, as with API tokens, so a dump of either store holds no
usable credentials.

## Security behaviour

- **Origin check** on `/login`, `/logout` and `/password`: a missing `Origin`
  passes (not a browser, so no CSRF); with `AUTH_ALLOWED_ORIGINS` set it must
  be in the list; otherwise it must equal the request's own origin, rebuilt
  from `X-Forwarded-Proto` / `X-Forwarded-Host`.
- **Rate limiting** on `/login`: a per-account and a per-address counter, both
  charged even when the first already refused. A success clears only the
  per-account one. On a Redis failure the limiter allows the request.
- **Cookie**: `HttpOnly`, `Secure` unless `AUTH_COOKIE_SECURE` is anything but
  `true`, `SameSite=Lax`, `Max-Age` = session TTL.
- The nginx in front must strip any `X-Principal-Id` the client sent; in the
  showcase `proxy_set_header` replaces it.

## Configuration

| variable | default | |
|---|---|---|
| `AUTH_DATABASE_URL` | required | PostgreSQL URL, e.g. `postgres://auth:…@auth_postgres:5432/auth?sslmode=disable` |
| `REDIS_ADDR` | `localhost:6379` | |
| `AUTH_PORT` | `8093` | |
| `AUTH_SESSION_TTL` | `168h` | also the cookie's Max-Age; slid forward on use |
| `AUTH_COOKIE_NAME` | `auth_session` | a `__Host-` name in production |
| `AUTH_COOKIE_SECURE` | `true` | |
| `AUTH_ALLOWED_ORIGINS` | empty | comma-separated; empty means same-origin only |
| `AUTH_LOGIN_RATE_LIMIT` | `10` | per-account attempts per window |
| `AUTH_LOGIN_IP_RATE_LIMIT` | `100` | per-address; loose, so an office behind one NAT cannot lock itself out |
| `AUTH_LOGIN_RATE_WINDOW` | `15m` | |
| `AUTH_SEED_USERNAME` / `AUTH_SEED_PASSWORD` / `AUTH_SEED_EMAIL` | empty | first account, created only while the table is empty |

## Operator commands

There is no admin surface. The image carries two commands next to the server:

- `/root/hashpw` reads a password on stdin and prints its hash, made by the
  same code that verifies it, under the same policy (10..128 characters). An
  account is then one `INSERT` into `accounts`.
- `/root/disable <username>` disables the account and kills its sessions at
  once; its API tokens stop working within a minute.

An API token is a random string whose SHA-256 goes into `api_tokens` with the
account it acts as, a name saying what it is for, and a required expiry.

## Image

Built from this directory. From another compose file, pinned to a tag:

```yaml
auth_service:
  build: https://github.com/jurasikasan/units.git#auth_service/v0.0.0:auth_service
```

## Checks

```bash
bash check.sh   # build + vet + fmt + lint + test + spec
```
