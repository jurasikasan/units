# lab

Units assembled into one place that runs: a site behind [`gate`](../../gate/),
an application behind the site, mail, and the state kept and restored. A
local run and a deployment are the same files; only `.env` differs.

For now the application is a page that says hello to whoever logged in.

## Run it

```bash
cp .env.example .env
docker compose up -d --wait
```

https://lab.localhost:8443/, `alice` / `alice-lab`. The certificate is
self-signed, so the browser asks first. Mail lands in Mailpit,
http://localhost:8025.

`./check.sh` runs the whole round from `.env.example` on free ports: up, a
person logs in and gets mail, a second account is added, a backup is taken,
everything is deleted with its volumes, the backup is restored, and the
second account logs in again.

## .env

| Variable | |
|---|---|
| `LAB_DOMAIN`, `LAB_URL` | the host name, and what a browser types (port included unless 443) |
| `LAB_BIND`, `LAB_PORT` | where the https port is published: `127.0.0.1` locally, `0.0.0.0` to be reached |
| `LAB_CERTS` | a directory with `fullchain.pem` and `privkey.pem`, or `certs`, a volume where a self-signed certificate is made on the first start |
| `LAB_FIRST_USER`, `LAB_FIRST_EMAIL`, `LAB_FIRST_PASSWORD` | the first account, made only when there is none; clear the password afterwards |
| `COMPOSE_PROFILES` | `mailpit` runs Mailpit locally; a deployment leaves it empty |
| `LAB_SMTP_*` | where mail goes; with a relay, `LAB_SMTP_SKIP_VERIFY=false` |

## Accounts

```bash
docker compose run --rm --no-deps -T gate gate-user add bob bob@example.com "Bob" < password.txt
```

## Backup and restore

```bash
./backup.sh [dir]            # → dir/lab-<time>.tar.gz, dir defaults to ./backups
./restore.sh <archive>       # replaces what the lab holds, then starts it
```

The archive holds gate's `/data` (accounts, passkeys, second factors) and
`/secrets` (its keys) together: gate does not start on one without the
other. Gate stops for the seconds the copy takes, so its database is copied
whole. Sessions are not kept; a restore logs everyone out.

## Not here yet

- tokens for programs: a machine calling the lab's API;
- mail to the outside world (UNIT-23);
- plain http on port 80 sending to https.
