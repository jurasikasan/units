# lab

Units assembled into one place that runs: a site behind [`gate`](../../gate/),
an application behind the site, mail, and the state kept and restored. A
local run and a deployment are the same files; only `.env` differs.

The application is a chat: [`faces/web_chat`](../../faces/web_chat/) at `/`,
[`minds/assistant`](../../minds/assistant/) at `/api/`, and LiteLLM in front
of the models; the model's tools come from MCP servers, for now MCP's own
reference server, `everything`. The lab is no unit but a consumer of units:
its own check is its showcase.

```
browser → nginx + gate ─ /      → face (static files)
                       └ /api/  → mind ─┬→ LiteLLM ─┬→ fake_llm (answers from files)
                                        │           └→ real providers
                                        └→ MCP: everything (a sum, an image, …)
```

## Models

`litellm/models.yaml` names them; the mind asks for `LAB_LLM_MODEL`.

| model | |
|---|---|
| `fake` | [`fake_llm`](fake_llm/): answers from files, the same each time, no key; what the checks run on |
| `chat` | Cloudflare's glm-4.7-flash, then z.ai's; a reasoning model, so 10–15 s of thinking before the first word |
| `lite` | Gemini 3.1 flash-lite |

A real model needs its provider's key in `.env`; one without a key fails
when asked, and nothing else does.

## Run it

```bash
cp .env.example .env
docker compose up -d --wait
```

https://lab.localhost:8443/, `alice` / `alice-lab`. The certificate is
self-signed, so the browser asks first. Mail lands in Mailpit,
http://localhost:8025.

`./check.sh` runs the whole round from `.env.example` on free ports: up; in a
real browser (Playwright, `e2e/chat.mjs`) a person logs in, says something,
asks what they said first and gets it back, which only happens when the
whole conversation goes page → AG-UI → mind → LiteLLM → model; a tool
adds two numbers; the model's reasoning is shown apart from its answer, and
the thought signatures a model left go back to it with the tool call and,
a run later, from the page; a file over the page's limit is refused there
and nothing is sent, then a picture and a text file go with a question and
the answer carries what only the file holds; the chat
refuses someone not logged in; a person gets mail, a second account is added, a backup is taken,
everything is deleted with its volumes, the backup is restored, and the
second account logs in again.

The same browser against a running lab and whatever model it asks, a real
one included (the answer is then the model's, so give it time):

```bash
E2E_TIMEOUT_MS=180000 docker compose run --rm e2e alice alice-lab
```

## .env

| Variable | |
|---|---|
| `LAB_DOMAIN`, `LAB_URL` | the host name, and what a browser types (port included unless 443) |
| `LAB_BIND`, `LAB_PORT` | where the https port is published: `127.0.0.1` locally, `0.0.0.0` to be reached |
| `LAB_CERTS` | a directory with `fullchain.pem` and `privkey.pem`, or `certs`, a volume where a self-signed certificate is made on the first start |
| `LAB_FIRST_USER`, `LAB_FIRST_EMAIL`, `LAB_FIRST_PASSWORD` | the first account, made only when there is none; clear the password afterwards |
| `COMPOSE_PROFILES` | `mailpit` runs Mailpit locally; a deployment leaves it empty |
| `LAB_SMTP_*` | where mail goes; with a relay, `LAB_SMTP_SKIP_VERIFY=false` |
| `LAB_LLM_KEY` | LiteLLM's master key; the mind sends it |
| `LAB_LLM_MODEL` | the model the mind asks for: `fake`, `chat`, `lite` |
| `LAB_LLM_CONFIG` | another model list than `./litellm/models.yaml` |
| `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_API_KEY`, `ZAI_API_KEY`, `GEMINI_API_KEY` | the providers' keys |
| `LAB_SYSTEM_PROMPT` | first in every prompt the mind sends |
| `LAB_MCP_SERVERS` | the MCP servers whose tools the model may call, `name=url,…`; `everything=http://everything:3001/mcp` when unset |

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

- the chat keeps nothing: one conversation per page, gone on reload;

- tokens for programs: a machine calling the lab's API;
- mail to the outside world (UNIT-23);
- plain http on port 80 sending to https.
