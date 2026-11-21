# lab

Units assembled into one place that runs: a site behind [`gate`](../../gate/),
an application behind the site, mail, and the state kept and restored. A
local run and a deployment are the same files; only `.env` differs.

The application is a chat: [`faces/web_chat`](../../faces/web_chat/) at `/`,
whose chats are threads written and followed through reins;
[`drivers/model`](../../drivers/model/), the brains: its profiles
(`LAB_PROFILES`) hold avatars' reins when a person hands them over in the
page (each account's assistant starts held by `LAB_DEFAULT_BRAIN`), and
answer in those threads through [`minds/assistant`](../../minds/assistant/)
(also at `/api/` for any AG-UI client), and LiteLLM in front of the models; the model's tools come from the MCP servers `LAB_MCP_SERVERS`
names, none by default. MCP's own reference server, `everything`, runs under
its compose profile for the checks or to try a tool. The lab is no unit but a consumer of units:
its own check is its showcase.

Who exists and who steers them: [`subjects`](../../subjects/) keeps the
subjects, [`reins`](../../reins/) at `/api/reins/` which of them a principal
(gate's username) steers. The first `GET /api/reins/me/subjects` after a
login gives the person two: `<login>` and `<login>-assistant`; later calls
answer the same ones. Each keeps its own Postgres on a network only it
joins. The face asks for them in its settings, and changes their names,
bios and preferences there (`PATCH /api/reins/subjects/{id}`).
[`threads`](../../threads/) keeps what happened where, each append
announced on the bus (`threads_events`). Nothing reaches it from outside:
reins is the face's door into it. The face's chats are threads: what the
person says goes in as the avatar whose reins the page holds, and every
entry of their threads comes back on `/api/reins/me/stream` (server-sent
events) as it is appended, whoever said it, so a second browser sees the
first's words without a reload, and a reload keeps the conversation.

Who holds each avatar's reins is reins' too: one holder at a time, the
person from a browser, a brain (one of the driver's profiles), or nobody.
On the first login the assistant is the default brain's, and a first chat
is opened by it, where it greets. The page lands as the ghost (the
person without a body: every thread of their avatars, nothing to do); the
picker at the top of its sidebar steps into an avatar, shows who holds
each, and its radio takes, gives back or hands it to a brain; a brain's
reply is stopped from the composer's row. A page gone longer than `LAB_HOLD_GRACE` lets go of what it held. The
driver reads whom its profiles hold from reins' inner port (8088), which
nginx never forwards.

Who is logged in is gate's to say: `/api/v1/userinfo` is its
`userinfo.conf`, and the face shows it at the foot of the sidebar with a
log-out that goes to gate's own `/auth/logout`.

```
browser → nginx + gate ─ /      → face (static files)
                       ├ /api/v1/userinfo → gate itself: who is logged in, where to log out
                       ├ /api/reins/ → reins: the person's subjects → subjects; their threads → threads
                       │          ↑ /me/stream follows the bus: threads_events, driver_events, reins_events
                       │  driver (profiles; holds what reins says, :8088) ← threads_events, reins_events; → threads, → mind
                       └ /api/  → mind ─┬→ LiteLLM ─┬→ fake_llm (answers from files)
                                        │           ├→ fake_realtime (a call, from a script)
                                        │           ├→ realtime (a call: hearing → a chat model → speaking)
                                        │           └→ real providers
                                        └→ MCP: LAB_MCP_SERVERS (profile everything: a sum, an image, …)
```

## Models

`litellm/models.yaml` names them; the driver's profiles (`LAB_PROFILES`)
each think with one: which model thinks is the brain holding an avatar's
reins, handed in the page, not the page's own (the composer has no pick). The
avatars' bodies are set in the page's settings (opened from who is logged
in, at the sidebar's foot), the same on each: an ear (`LAB_STT_MODEL` when
none), a mouth (`LAB_TTS_MODEL`) and the mouth's voices, where its engine
lists them; realtime ones are not offered. What is set is kept in
`subjects`, through reins, and holds across reloads and logins. A group whose key or URL is empty is offered too and
fails when asked.

| model | |
|---|---|
| `fake` | [`fake_llm`](fake_llm/): answers from files, the same each time, no key; what the checks run on |
| `fake-text` | the same, declared to take words only: a picture to it is refused by the mind |
| `chat` | Cloudflare's glm-4.7-flash, then z.ai's; a reasoning model, so 10–15 s of thinking before the first word; words only |
| `lite` | Gemini 3.1 flash-lite |
| `fake-stt`, `fake-tts` | [`fake_llm`](fake_llm/) behind OpenAI's audio API: any sound is heard as `What the fake ear heard.`, any words are a quarter-second beep |
| `fake-stt-b`, `fake-tts-b` | second fakes, to see a pick: `What the second fake ear heard.`, a tenth of a second of a lower tone in voice `denis` or `irina` (listed), another voice refused |
| `stt` | Groq's whisper-large-v3-turbo: what the browser records, any language; then `whisper-gpu` when Groq fails |
| `whisper-gpu` | [whisper](../../engines/stt/whisper/) large-v3 on a GPU box, at `LAB_STT_GPU_URL` |
| `piper`, `silero` | [our engines](../../engines/tts/) on this machine, under the `engines` profile: WAV, their own voices (listed), Russian and English |
| `voxcpm` | [VoxCPM2](../../engines/tts/voxcpm/) on a GPU box, at `LAB_VOXCPM_URL`: WAV at 48 kHz, cloned voices (listed) |
| `images`, `images-fast` | [pictures](../../engines/images/cloudflare/) drawn at Cloudflare (FLUX.2 klein 4B, FLUX.1 schnell) through OpenAI's Images API, under the `engines` profile, on the Cloudflare account in `.env` |
| `local` | gpt-oss:20b on the GPU box's Ollama, at `LAB_OLLAMA_URL` |
| `tts` | Gemini 3.8 flash-lite TTS: WAV only, Gemini's voices (OpenAI's names map to some); to try, not to run on |
| `fake-realtime` | [`fake_realtime`](fake_realtime/) behind OpenAI's Realtime API: hears by loudness, answers from a script with a tone, in the event flow Gemini Live sent through LiteLLM |
| `fake-realtime-full` | the same, in the schema's whole set (`speech_started`, a cancelled answer, `truncated`) |
| `cascade` | our own: [`minds/realtime`](../../minds/realtime/) made of [`senses/hearing`](../../senses/hearing/), a chat model and [`abilities/speaking`](../../abilities/speaking/), all through this LiteLLM, the ear and voice the avatars' bodies have in the page's settings, the chat model `LAB_CASCADE_LLM_MODEL`, `LAB_CASCADE_*` when the page names none; the language found in each utterance; the whole set, truncate honoured |

The mind serves OpenAI's audio API at `/api/v1/audio/…` and hands it on with
`LAB_STT_MODEL` and `LAB_TTS_MODEL`: the page's mic puts what was said into
the composer, and an answer can be read aloud. A call's transcripts show
in the chat for as long as the page is open: they are not journaled yet
(UNIT-52), so nobody else sees them, the driver does not, and a reload
loses them. A call goes to
`/api/v1/realtime`, a WebSocket nginx keeps open (an hour without a
message ends it), which the mind relays to LiteLLM's with
`LAB_REALTIME_MODEL`; the call button shows only then. No realtime model
on a key we hold is to be run on (Gemini Live is for tests only), so the lab
has the fakes and our own, `cascade`. Its organs' events go on a Redis of
their own, `bus`; hearing finds speech by loudness unless
`LAB_CASCADE_VAD=silero` (which needs silero's VAD at `vad`).
nginx writes its site from `site.conf.template`, and LiteLLM reads
`models.yaml`, only when they start: after pulling a change to either (the
call came with both), `docker compose up -d --force-recreate nginx litellm`;
otherwise the call ends at once with 1006, or LiteLLM says it has no such
model.

What a model takes (pictures, PDFs, sounds) LiteLLM says from its map of
models, or from `model_info` in `models.yaml` where the map does not know
the model (`chat`); the mind refuses what it does not take.

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
conversation goes page → reins → threads → driver → mind → LiteLLM → model,
the reply streamed back on reins' stream as it is written; a tool adds two
numbers; the reply said is the answer without its reasoning, and the
thought signatures a model left go back to it with the tool call within the
run (a run later they are gone: not what was said, so not journaled); a
file over the page's limit is refused there and nothing is written, then a
picture and a text file go with a question and the answer carries what only
the file holds; a model answering 429 or 500 is said on the page while
written and gone after a reload (never journaled); the composer offers no
chat model and the driver's answers, an avatar's name set in the settings
is still there after a reload, and every avatar offers the same fields
(`E2E_ONLY=model`); a new chat in the sidebar starts empty and is a thread
of its own, the first is all there when switched back to, and a reload
keeps the conversation (`E2E_ONLY=threads`); two browsers, one as `alice`
and one as `alice-assistant` (that one speaking through reins' route: the
page acts only as the login's avatar), each see the other's words without a
reload (`E2E_ONLY=two`); the second fake ear
set on the person and the second fake mouth and one of its voices on the
assistant, in the settings, are what answer (`E2E_ONLY=sound`); the mic (the browser's fake
one) puts what the fake ear heard into the composer and an answer is read
aloud to the end; on the driver recreated to hold the assistant with
`fake-text`, a picture is refused and the page says the model takes none
(`E2E_ONLY=refused`); the chat
refuses someone not logged in; after the first login the person steers
`alice` and `alice-assistant`, and the same two on the next call; a person gets mail, a second account is added, a backup is taken,
everything is deleted with its volumes, the backup is restored, and the
second account logs in again, and the first steers the same subjects as
before.

The same browser against a running lab and whatever model it asks, a real
one included (the answer is then the model's, so give it time):

```bash
E2E_TIMEOUT_MS=180000 docker compose run --rm e2e alice alice-lab
```

Sound alone, on the real `stt` and `tts`, with a recording of speech for
the fake mic to play (a WAV):

```bash
docker compose run --rm -v "$PWD/speech.wav:/voice.wav:ro" -e E2E_VOICE_FILE=/voice.wav -e E2E_ONLY=voice e2e alice alice-lab
```

A call alone (`E2E_ONLY=call`): the fake mic's tone is an utterance every
three seconds, so the second answer is interrupted by the next. On our own:

```bash
LAB_REALTIME_MODEL=cascade docker compose up -d --wait mind
LAB_REALTIME_MODEL=cascade E2E_ONLY=call docker compose run --rm e2e alice alice-lab
```

With real parts: set them in the settings before the call (a mouth whose
voices are listed: `piper`, `silero`, `voxcpm`); what is set holds for the
call.


## .env

| Variable | |
|---|---|
| `LAB_DOMAIN`, `LAB_URL` | the host name, and what a browser types (port included unless 443) |
| `LAB_BIND`, `LAB_PORT` | where the https port is published: `127.0.0.1` locally, `0.0.0.0` to be reached |
| `LAB_CERTS` | a directory with `fullchain.pem` and `privkey.pem`, or `certs`, a volume where a self-signed certificate is made on the first start |
| `LAB_FIRST_USER`, `LAB_FIRST_EMAIL`, `LAB_FIRST_PASSWORD` | the first account, made only when there is none; clear the password afterwards |
| `LAB_PROGRAM`, `LAB_PROGRAM_PASSWORD` | a program's account, made when it is missing (its mail `<name>@LAB_DOMAIN`); the program sends the password as HTTP Basic on `/api/`; keep it, the program needs it; empty: none |
| `COMPOSE_PROFILES` | `mailpit` runs Mailpit locally; a deployment leaves it empty |
| `LAB_SMTP_*` | where mail goes; with a relay, `LAB_SMTP_SKIP_VERIFY=false` |
| `LAB_LLM_KEY` | LiteLLM's master key; the mind sends it |
| `LAB_LLM_MODEL` | the mind's own model: `fake`, `chat`, `lite`; the default brain's when `LAB_DEFAULT_BRAIN` is empty |
| `LAB_PROFILES` | the brains, the driver's profiles, `<name>=<model>,…`: `fake=fake,fake-text=fake-text,chat=chat` |
| `LAB_DEFAULT_BRAIN` | the profile each account's assistant is handed to at its first login |
| `LAB_HOLD_GRACE` | how long a page may be gone before what it held is let go: `2m` |
| `LAB_LLM_CONFIG` | another model list than `./litellm/models.yaml` |
| `LAB_STT_MODEL`, `LAB_TTS_MODEL` | the models the mind asks to transcribe and to speak when the page names none, shown first in the settings: `fake-stt`/`stt`/`whisper-gpu`, `fake-tts`/`tts`/`piper`/`silero`/`voxcpm`; empty, the page has no mic or read-aloud |
| `LAB_TTS_VOICE` | `LAB_TTS_MODEL`'s voice when the page names none; OpenAI's speech wants one |
| `LAB_STT_GPU_URL`, `LAB_VOXCPM_URL`, `LAB_OLLAMA_URL` | the GPU engines, each with `/v1` (Ollama without); empty, their models fail when asked |
| `LAB_SILERO_ACCEL` | silero's image: `xpu` (falls back to CPU), `cuda` |
| `LAB_REALTIME_MODEL` | the model a call goes to: `fake-realtime`, `fake-realtime-full`, `cascade`; empty, the page has no call button |
| `LAB_CASCADE_STT_MODEL`, `LAB_CASCADE_LLM_MODEL`, `LAB_CASCADE_VOICE`, `LAB_CASCADE_PROMPT`, `LAB_CASCADE_VAD` | `cascade`'s parts when the page names none: hearing's ear (a model here), the chat model, speaking's `<model>_<voice>`; the instructions; `energy` or `silero` |
| `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_API_KEY`, `ZAI_API_KEY`, `GEMINI_API_KEY`, `GROQ_API_KEY` | the providers' keys |
| `LAB_SYSTEM_PROMPT` | first in every prompt the mind sends |
| `LAB_MCP_SERVERS` | the MCP servers whose tools the model may call, `name=url,…`; none when empty. `everything=http://everything:3001/mcp` with `everything` in `COMPOSE_PROFILES` gives the model MCP's reference server, as `check.sh` does |

## Accounts

```bash
docker compose run --rm --no-deps -T gate gate-user add bob bob@example.com "Bob" < password.txt
```

### Programs

A program (a script, an agent) is an account like a person's, and behind
the gate it is `X-Principal-Id = <its name>`; what it may do is what that
account steers. It passes on `/api/` only, with its password as HTTP Basic
(gate's README: temporary until the gate issues tokens); pages stay closed to
it:

```bash
curl -u "$LAB_PROGRAM:$LAB_PROGRAM_PASSWORD" "$LAB_URL/api/reins/me/subjects"
```

`LAB_PROGRAM` makes one; more are added as accounts, above. A changed
password is a new account: remove the entry from gate's `/data/users.yml`
and start again.

## Backup and restore

```bash
./backup.sh [dir]            # → dir/lab-<time>.tar.gz, dir defaults to ./backups
./restore.sh <archive>       # replaces what the lab holds, then starts it
```

The archive holds gate's `/data` (accounts, passkeys, second factors) and
`/secrets` (its keys) together: gate does not start on one without the
other; and `pg/`, dumps of subjects' and reins' databases. Gate, subjects
and reins stop for the seconds the copy takes, so each is copied whole and
the two databases agree. An archive from before they joined restores gate
and leaves their databases as they are. Sessions are not kept; a restore
logs everyone out.

## Not here yet

- the chat keeps nothing: one conversation per page, gone on reload;

- tokens for programs: a machine calling the lab's API;
- mail to the outside world (UNIT-23);
- plain http on port 80 sending to https.
