# lab

Units assembled into one place that runs: a site behind [`gate`](../../gate/),
an application behind the site, mail, and the state kept and restored. A
local run and a deployment are the same files; only `.env` differs.

The application is a chat: [`faces/web_chat`](../../faces/web_chat/) at `/`,
[`minds/assistant`](../../minds/assistant/) at `/api/`, and LiteLLM in front
of the models; the model's tools come from the MCP servers `LAB_MCP_SERVERS`
names, none by default. MCP's own reference server, `everything`, runs under
its compose profile for the checks or to try a tool. The lab is no unit but a consumer of units:
its own check is its showcase.

Who is logged in is gate's to say: `/api/v1/userinfo` is its
`userinfo.conf`, and the face shows it at the foot of the sidebar with a
log-out that goes to gate's own `/auth/logout`.

```
browser → nginx + gate ─ /      → face (static files)
                       ├ /api/v1/userinfo → gate itself: who is logged in, where to log out
                       └ /api/  → mind ─┬→ LiteLLM ─┬→ fake_llm (answers from files)
                                        │           ├→ fake_realtime (a call, from a script)
                                        │           ├→ realtime (a call: hearing → a chat model → speaking)
                                        │           └→ real providers
                                        └→ MCP: LAB_MCP_SERVERS (profile everything: a sum, an image, …)
```

## Models

`litellm/models.yaml` names them; the mind asks for `LAB_LLM_MODEL` unless
the person picks another in the page: the pickers in the sidebar offer the
chat groups (`LAB_LLM_MODEL` chosen), the ears (`LAB_STT_MODEL` chosen),
the mouths (`LAB_TTS_MODEL` chosen) and the picked mouth's voices, where
its engine lists them; realtime ones are not offered. Picks hold until the
page is reloaded. A group whose key or URL is empty is offered too and
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
| `local` | gpt-oss:20b on the GPU box's Ollama, at `LAB_OLLAMA_URL` |
| `tts` | Gemini 3.8 flash-lite TTS: WAV only, Gemini's voices (OpenAI's names map to some); to try, not to run on |
| `fake-realtime` | [`fake_realtime`](fake_realtime/) behind OpenAI's Realtime API: hears by loudness, answers from a script with a tone, in the event flow Gemini Live sent through LiteLLM |
| `fake-realtime-full` | the same, in the schema's whole set (`speech_started`, a cancelled answer, `truncated`) |
| `cascade` | our own: [`minds/realtime`](../../minds/realtime/) made of [`senses/hearing`](../../senses/hearing/), a chat model and [`abilities/speaking`](../../abilities/speaking/), all through this LiteLLM, each the one picked in the sidebar (model, speech to text, text to speech and voice), `LAB_CASCADE_*` when none is; the language found in each utterance; the whole set, truncate honoured |

The mind serves OpenAI's audio API at `/api/v1/audio/…` and hands it on with
`LAB_STT_MODEL` and `LAB_TTS_MODEL`: the page's mic puts what was said into
the composer, and an answer can be read aloud. A call goes to
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
whole conversation goes page → AG-UI → mind → LiteLLM → model; a tool
adds two numbers; the model's reasoning is shown apart from its answer, and
the thought signatures a model left go back to it with the tool call and,
a run later, from the page; a file over the page's limit is refused there
and nothing is sent, then a picture and a text file go with a question and
the answer carries what only the file holds; the picker offers the chat
groups with `fake` chosen, and picking `fake-text` is what answers the next
run, `fake` again the one after (`E2E_ONLY=model`); a new chat in the
sidebar starts empty and runs as a thread of its own, and the first is all
there when switched back to (`E2E_ONLY=threads`); the second fake ear,
mouth and one of its voices picked in the sidebar are what answer
(`E2E_ONLY=sound`); the mic (the browser's fake
one) puts what the fake ear heard into the composer and an answer is read
aloud to the end; on the mind recreated to ask
`fake-text`, a picture is refused and the page says the model takes none
(`E2E_ONLY=refused`); the chat
refuses someone not logged in; a person gets mail, a second account is added, a backup is taken,
everything is deleted with its volumes, the backup is restored, and the
second account logs in again.

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

With real parts: pick them in the sidebar before the call (a mouth whose
voices are listed: `piper`, `silero`, `voxcpm`); what is picked holds for
the call.


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
| `LAB_LLM_MODEL` | the model the mind asks for when the page picks none, chosen first in its picker: `fake`, `chat`, `lite` |
| `LAB_LLM_CONFIG` | another model list than `./litellm/models.yaml` |
| `LAB_STT_MODEL`, `LAB_TTS_MODEL` | the models the mind asks to transcribe and to speak when the page picks none, chosen first in its pickers: `fake-stt`/`stt`/`whisper-gpu`, `fake-tts`/`tts`/`piper`/`silero`/`voxcpm`; empty, the page has no mic or read-aloud |
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
