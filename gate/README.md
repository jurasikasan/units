# gate

The door in front of applications. A person logs in on a page at `/auth`;
nginx asks before serving anything protected whether the request carries a
session, or a program's token; the application behind gets one header,
`X-Principal-Id`, and never sees a password, a cookie or a token.

Almost none of it is our code. Accounts, sessions, the login page, passkeys
and programs' tokens are [Authelia](https://www.authelia.com) (Apache-2.0);
the asking is nginx `auth_request`. The unit is the configuration that makes
the two one contract: an image with Authelia's configuration rendered from
the environment, and nginx snippets a site includes. Ours is a small client
for programs, `client/gate-token`.

## Showcase

```bash
cd showcase
docker compose up -d --wait
docker compose run --rm demo
```

Authelia with Redis for sessions, Mailpit catching its mail, nginx with a
self-signed certificate, and a page behind it that says hello to whoever
passed. A one-shot `seed` makes the accounts: alice and bob, people, and
robot, a program's. The demo walks through with curl, and with
`client/gate-token` as the program; the person who lets it in is curl too:

```
no session
  a page sends to the login page             302
  an API answers 401                         401
  a forged X-Principal-Id changes nothing    401
  who is logged in: nobody                   401
  a 2 MB upload answers 401, not 500         401
a program, robot: a person lets it in once
  the program prints an address at the gate  ok
    … opened, it asks to log in first      ok
    … the person logs in as robot          200
    … is asked to let it in                "scopes":["offline_access","authelia.bearer.authz"]
    … allows it, and the browser posts to  https://gate.localhost:8443/gate/code
    … that page, without a session: login  302
    … with it, shows the code              authelia_ac_
  the program keeps a refresh token, 0600    1 600
the program on the API with an access token
  X-Principal-Id (client sent mallory)       robot
  Authorization                              []
  a wrong token on an API                    401
  a page does not open, to the login page    302
  who is logged in: nobody                   401
  its password as HTTP Basic: not taken      401
  … the access token ends (10 s in the showcase)
  the old one on the API                     401
  gate-token refreshes it, no person         new
    … and the API answers robot            robot
    … and keeps the new refresh token      new
  a password as Basic opens no page          302
log in
  wrong password                             401
  alice                                      200
    … sent on to the application           https://gate.localhost:8443/
what the application sees
  the page                                   hello, alice
  X-Principal-Id (client sent mallory)       alice
  Cookie                                     []
  Authorization (client sent a Bearer)       []
  a 2 MB upload reaches the application      204
who is logged in, for the person's page
  userinfo                                   {"sub":"alice",…,"end_session_endpoint":"/auth/logout"}
mail
  a one-time code mailed to alice            [Authelia] Confirm your identity
log out
  the API is closed again                    401
  userinfo too                               401
another person
  bob                                        200
  the page                                   hello, bob
  X-Principal-Id                             bob
  alice's password is not bob's              401
```

It ends logged in again; `docker compose restart gate` and then
`docker compose run --rm demo session` show the session and the program's
refresh token outlive the restart.

In a browser: https://gate.localhost:8443/, `alice` / `alice-showcase` or
`bob` / `bob-showcase`; a program's is `robot` / `robot-showcase` (showcase passwords, in `showcase/compose.yaml`). The certificate is
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
  `GATE_URL/`. An API answers a plain 401.
- A program is a principal like a person: an account of its own, and behind
  the gate `X-Principal-Id` is that account's name. It passes on API paths
  only (`api.conf`), with a short-lived Bearer access token (see
  [Programs](#programs)). Only a session opens a page or userinfo; an
  `Authorization` header there is not looked at.
- Who is logged in is told to the person's own page, not to the
  application: where the site includes `userinfo.conf`, OpenID Connect's
  UserInfo claims (`sub` and `preferred_username` the username, `name`,
  `email`) and, beside them, `end_session_endpoint`, OIDC's name for where
  logging out happens (Authelia's `/auth/logout`). Without a session, 401.

## Putting it in front of an application

The site's nginx includes the snippets in `nginx/` (mounted at, say,
`/etc/nginx/gate/`) and reaches the unit as `gate:9091`:

```nginx
server {
    listen 443 ssl;
    resolver 127.0.0.11 valid=1s;               # the site's DNS; Docker's here
    include /etc/nginx/gate/authz.conf;          # once per server

    location /auth { include /etc/nginx/gate/portal.conf; }
    location = /gate/code { include /etc/nginx/gate/code.conf; }
    location = /api/v1/userinfo { include /etc/nginx/gate/userinfo.conf; }
    location /api/ { include /etc/nginx/gate/api.conf;  set $app app; proxy_pass http://$app; }
    location /     { include /etc/nginx/gate/page.conf; set $app app; proxy_pass http://$app; }
}
```

The snippets name gate through a variable, so nginx looks it up as requests
come rather than once at start, and gate recreated on another address is
found there without restarting nginx. That needs a `resolver` in the site,
which knows its own DNS; without one, nginx cannot reach gate and every
protected request fails. `valid=` is how long an answer is kept: for that
long after gate moves, requests still go to its old address, so keep it
short (a second costs one lookup a second). The
application is best named the same way, for the same reason. A `proxy_pass`
with a variable passes the request's path as it is, so a prefix to strip
takes a `rewrite … break` (stacks/lab's `/api/` does).

`userinfo.conf` answers from nginx alone: the claims come from Authelia's
answer to the session check, and a value that would break the JSON (a
quote, a backslash; `gate-user` makes none) is refused with a 500 rather
than escaped.

A location that includes `page.conf` or `api.conf` takes its
`proxy_set_header` lines from there; nginx does not merge them with the
server's.

How large a body may be is the protected location's own
`client_max_body_size` (nginx's default is 1 MB). The check is not held to
it: the subrequest gets no body and takes any size, since nginx would
otherwise answer a larger one with 413 there, and `auth_request` turns that
into 500.

## Programs

A program (a script, an agent, this repo's Claude) gets an account, as a
person does, named for it:

```bash
docker compose run --rm gate gate-user add claude-units claude-units@example.com < password.txt
```

It can do what that account steers and nothing else; applications cannot
tell it from a person and need not. It does not send the password, though:
a person lets it in once, and from then on it holds tokens.

```bash
gate-token login --gate https://site.example --file ~/.config/claude-units/gate.json
curl -H "Authorization: Bearer $(gate-token token --file ~/.config/claude-units/gate.json)" https://site.example/api/…
```

1. `login` prints an address at the gate and waits for a code.
2. A person opens it in any browser, logs in **as the program's account**
   (its password stays with the person), and allows the program in.
3. The browser lands on the site's `/gate/code` (`code.conf`), a page that
   shows the code; the person pastes it to the program and logs out.
4. The program exchanges the code for an access token and a refresh token
   and keeps them in the file it was told (`0600`).
5. `token` prints the access token, and when it is near its end gets a new
   one with the refresh token, with no person. Each refresh also gives a new
   refresh token, kept in the file; the old one is refused from then on, and
   using it again revokes what was made from it. So a file is one program's,
   and `gate-token` locks it while refreshing.

`client/gate-token` is Python's standard library only: copy it where the
program runs, or import it and call `access_token(path)`. `GATE_URL`,
`GATE_TOKEN_FILE`, `GATE_CACERT` and `GATE_CONNECT_TO` stand in for its
flags.

Underneath is OAuth 2.0's authorization code grant with PKCE, a pushed
authorization request and `form_post`, from gate's own Authelia, for one
public client, `programs`: who a token speaks for is the account that
allowed it, not the client. It is the one route Authelia 4.39 leaves: a
token passes the door (scope `authelia.bearer.authz`) only from a client
that is PKCE + PAR + `form_post` with explicit consent; the device flow may
not hold that scope, and a `client_credentials` token passes with no
`Remote-User`. The code is shown on a page, not sent to the program,
because the program has no listener and maybe no browser. A code without
the program's PKCE verifier is worth nothing, is good for ten minutes and
once, and the page shows it only with a session.

Any account can let a program in this way, a person's own too: the tokens
then speak for that person. Taking a program out is deleting its account:
its tokens, refreshed or not, no longer pass the door.

How long tokens live is the deployment's: `GATE_TOKEN_LIFESPAN` (access,
10 minutes) and `GATE_REFRESH_LIFESPAN` (refresh, 30 days). A program that
refreshes within that time keeps going; one that sleeps longer is let in
again by a person.

### HTTP Basic, by a deployment's choice

`GATE_API_BASIC=true` lets a program also pass APIs with its account's
password as HTTP Basic (`curl -u name:password`), as gate did before tokens.
It is off unless said, and what it costs is why: the password travels on
every request and is checked every time (argon2, some 45 ms); three wrong
ones in two minutes refuse the account for five, even with the right one;
nothing expires. Pages never take it.

## Configuration

| Variable | |
|---|---|
| `GATE_DOMAIN` | the session cookie's domain, e.g. `gate.localhost` |
| `GATE_URL` | the site's public https base, port included, e.g. `https://gate.localhost:8443`; the login page is at `GATE_URL/auth` |
| `GATE_REDIS_HOST` | Redis for sessions, port 6379 |
| `GATE_SMTP_ADDRESS` | where mail goes, e.g. `submission://smtp.example.com:587` |
| `GATE_SMTP_SENDER` | the From of every mail, e.g. `gate <gate@example.com>` |
| `GATE_SMTP_USERNAME`, `GATE_SMTP_PASSWORD` | the relay's login, when it asks for one |
| `GATE_TOKEN_LIFESPAN` | a program's access token, `10m` unless said |
| `GATE_REFRESH_LIFESPAN` | a program's refresh token, `30d` unless said |
| `GATE_API_BASIC` | `true`: programs may also pass APIs with a password as HTTP Basic; off unless said |

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
| `/data/db.sqlite3` | passkeys, second factors, failed-login records, programs' tokens; the sensitive columns encrypted with the storage key |
| `/secrets/` | the session, storage and reset keys, the token signing key and HMAC secret, made on the first start |

`/data` and `/secrets` are backed up and restored together: Authelia refuses
to start on a database made with another storage key. Sessions are in Redis
and need no backup: losing them logs everyone out, nothing more.
