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
names: by default [`tools/images`](../../tools/images/), a tool that draws
(`images__draw`), through LiteLLM's `LAB_IMAGES_MODEL` (`fake-images`, a red
circle, no cloud; `images` at Cloudflare), its picture shown on the call's
card in the page as the tool's; and [`tools/web`](../../tools/web/), a tool
that looks at the web (`web__search`, by the lab's own SearXNG, a
metasearch with no index and no key; `web__fetch`, a public page's words),
whose fetching runs in `tool-web-fetcher` on `web-egress`, a network that
goes out and holds nothing of the stack but `tool-web` and `searxng`. MCP's own reference server, `everything`,
runs under its compose profile for the checks or to try a tool. The lab is no unit but a consumer of units:
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
                                        └→ MCP: LAB_MCP_SERVERS: tool-images (draw → LiteLLM's images API),
                                                tool-web (search → searxng; fetch → tool-web-fetcher; both on web-egress → the internet),
                                                (profile everything: a sum, an image, …)
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
| `fake-text` | the same, declared to take words only: a picture to it goes as a placeholder in words (the mind), which fake_llm answers `I am told of a picture, not shown it.` |
| `chat` | Cloudflare's glm-4.7-flash, then `chat-zai` (z.ai's) when Cloudflare fails; a reasoning model, so 10–15 s of thinking before the first word; words only |
| `chat-zai` | z.ai's glm-4.7-flash, one request at a time; `chat`'s fallback, and can be picked itself |
| `lite` | Gemini 3.1 flash-lite |
| `fake-stt`, `fake-tts` | [`fake_llm`](fake_llm/) behind OpenAI's audio API: any sound is heard as `What the fake ear heard.`, any words are a quarter-second beep |
| `fake-stt-b`, `fake-tts-b` | second fakes, to see a pick: `What the second fake ear heard.`, a tenth of a second of a lower tone in voice `denis` or `irina` (listed), another voice refused |
| `stt` | for a voice message (minutes, a person waiting): Groq's whisper-large-v3-turbo, what the browser records, any language; then `whisper-gpu` when Groq fails |
| `whisper-gpu` | [whisper](../../engines/stt/whisper/) large-v3 on a GPU box, at `LAB_STT_GPU_URL`, up to 2 min |
| `stt-call`, `stt-call-groq` | for an utterance in a call (`LAB_CASCADE_STT_MODEL`, hearing waits 15 s): whisper on the GPU box, then Groq's, 6 s each |
| `piper`, `silero` | [our engines](../../engines/tts/) on this machine, under the `engines` profile: WAV, their own voices (listed), Russian and English |
| `voxcpm` | [VoxCPM2](../../engines/tts/voxcpm/) on a GPU box, at `LAB_VOXCPM_URL`: WAV at 48 kHz, cloned voices (listed) |
| `images`, `images-fast` | [pictures](../../engines/images/cloudflare/) drawn at Cloudflare (FLUX.2 klein 4B, FLUX.1 schnell) through OpenAI's Images API, under the `engines` profile, on the Cloudflare account in `.env` |
| `fake-images` | the same API answered from `fake_llm`: any prompt is one picture, a red circle (128×128 PNG); what the draw tool uses by default and in the checks |
| `images-local` | commented out, with its `images-local` service: [diffusers](../../engines/images/diffusers/) on our own GPU, offline, for a machine that can hold it |
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

Every group in `models.yaml` says how long it may take beside how long its
caller waits: nothing is retried, a failure or a timeout goes to the
group's fallback (another provider), and a 429 falls through at once
instead of waiting out its Retry-After. What the free tiers behind the
keys allow, and what each is good for: [`litellm/FREE_TIERS.md`](litellm/FREE_TIERS.md).

## Run it

```bash
cp .env.example .env
docker compose up -d --wait
```

https://lab.localhost:8443/, `alice` / `alice-lab`. The certificate is
self-signed, so the browser asks first. Mail lands in Mailpit,
http://localhost:8025.

`./check.sh` runs the whole round from `.env.example` on free ports: up; in a
real browser, scenario by scenario (below),
real browser (Playwright, `e2e/chat.mjs`) a person logs in, says something,
asks what they said first and gets it back, which only happens when the
conversation goes page → reins → threads → driver → mind → LiteLLM → model,
the reply streamed back on reins' stream as it is written; a tool adds two
numbers; asked to draw, the assistant calls the draw tool and its message
holds the call's card with the picture, the tool's, kept on reload
(`E2E_ONLY=draw`); the reply said is the answer without its reasoning, and the
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
`fake-text`, a picture goes to the model as words and its answer says so
(`E2E_ONLY=refused`); the chat
refuses someone not logged in; after the first login the person steers
`alice` and `alice-assistant`, and the same two on the next call; a person gets mail, a second account is added, a backup is taken,
everything is deleted with its volumes, the backup is restored, and the
second account logs in again, and the first steers the same subjects as
before; then the backups leave the box (below, "Off the box", run here
without SSH): a fresh dump and the store's bytes pulled and checked by
sha256, pulled again after the box lost its first archive and a byte and
both still kept, a copy a day old called stale on the box and here, and a
third account, `dave`, back after a wipe from the pulled copy alone.

The images built from units are tagged `${UNITS_TAG:-local}`: a plain run
builds and runs `units-*:local`. `check.sh` sets `UNITS_TAG` to its own
compose project's name, so two checks run at once from two checkouts (agents
in worktrees) each build and run their own code, never the other's, and the
images it tagged are removed when it ends (their layers stay in the build
cache). gate's check does the same with its showcase.

### Scenarios

The browser's part is cut into scenarios, each a run of its own (a fresh
browser and login) and a line of its own in `check.sh`'s report,
`✓ e2e <name>: …` or `✗ e2e <name>: …`. One runs alone by name,
`E2E_ONLY=<name>`, against a running lab:

| Scenario | |
|---|---|
| `greet` | a login lands as the ghost, who sees every thread and does nothing, then steps into its avatar; on a first login the assistant has greeted first |
| `conversation` | page → reins → threads → driver → mind → model and back, streamed; a tool, reasoning, attachments, a file over the limit, a model answering 429 and 500 |
| `draw` | the fake model calls `images__draw`; the assistant's message holds a tool card (the tool, the prompt, the picture captioned as the tool's), its words beside it; a reload keeps it (fake model). With `E2E_SHOT` the page and the card are pictured |
| `stop` | a reply stopped is kept as far as it went, an empty one not at all (fake model) |
| `model` | the driver's model answers, no pick in the composer; a name set survives a reload; avatars alike (fake model) |
| `threads` | chats apart, the first all there when switched back to, kept on reload with `/t/<thread>` |
| `voice` | the mic into the composer, an answer read aloud to the end, a refusal said |
| `sound` | the ear, mouth and voice picked in the settings are what answer (fake ear) |
| `call` | a call through the mind, interrupted, muted, hung up (`LAB_REALTIME_MODEL` set) |
| `me` | who is logged in at the sidebar's foot; log-out ends the session |
| `two` | two browsers of one person talk, hand the assistant over and back, rename and delete a chat, leave |
| `refused` | handed to a text-only profile, a picture reaches the model as a placeholder in words, the run goes on |
| `failed` | a model answering 429 and 500, alone (fake model) |
| `factory` | the settings' "+" makes a new avatar: the nick follows the name, its own form opens, the picker lists it without a reload; renamed there, the picker shows it; the same nick again is refused under the nick in plain words. With `E2E_SHOT` the form is pictured, at the browser's width and a phone's. Last in `check.sh`: it adds to the two subjects the rounds before count |

Without `E2E_ONLY`, the browser runs `conversation` to `me` in one go
(`greet` first with `E2E_FIRST_LOGIN=1`).

A flaky step is caught on purpose, not in passing: `e2e/repeat.sh
<scenario> <n>` runs one scenario n times against the running lab the
environment names and tallies the outcomes, each run's output kept in
`REPEAT_LOGS`. Besides the browser's, two scenarios of its own: `greet`
makes a fresh account each run, so each is a first login, and `move`
recreates gate, mind and face on new addresses (`scripts/move_address.sh`)
and logs in through nginx. Run it beside whatever loads the machine:

```bash
COMPOSE_ENV_FILES=$PWD/.env.mine COMPOSE_PROJECT_NAME=mine docker compose up -d --wait
COMPOSE_ENV_FILES=$PWD/.env.mine COMPOSE_PROJECT_NAME=mine e2e/repeat.sh greet 20
# greet 1/20: pass in 8 s, load 6.10
# …
# greet: 20 of 20 passed, 0 failed (logs in /tmp/repeat-…)
```

The browser runs on the lab's own network, not the host's: the address bar
says `LAB_URL`, but its host name is the container's loopback, where a
door leads to nginx (`E2E_DOOR`). On the host's network every bridge Docker
adds or removes, another lab brought up or down beside it, was a network
change to Chromium, which aborted the page it was loading
(`net::ERR_NETWORK_CHANGED`); the lab's own network changes under nobody.

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
| `LAB_MCP_SERVERS` | the MCP servers whose tools the model may call, `name=url,…`, every avatar's brain all of them; none when empty. `images=http://tool-images:8080/mcp,web=http://tool-web:8080/mcp` by default, the draw tool and the web's search and fetch; `,everything=http://everything:3001/mcp` added, with `everything` in `COMPOSE_PROFILES`, gives the model MCP's reference server too, as `check.sh` does |
| `LAB_SEARXNG_URL` | the web tool's search engine: `http://searxng:8080`, the lab's own SearXNG (a metasearch: no index, no key, it asks public engines, which may rate-limit it); also when the line is missing |
| `BRAVE_API_KEY` | Brave Search API's token, asked when `LAB_SEARXNG_URL` is empty; both empty, `web__search` answers that it is not configured and `web__fetch` works all the same |
| `LAB_IMAGES_MODEL`, `LAB_IMAGES_FAST_MODEL` | the models the draw tool asks LiteLLM for, the second for a call that wants it fast (empty: the first): `fake-images`; `images`, `images-fast` at Cloudflare (`engines` profile) |

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
other; and `pg/`, dumps of subjects', reins' and threads' databases. Gate
and those three stop for the seconds the copy takes, so each is copied whole
and the databases agree. An archive from before they joined restores gate
and leaves their databases as they are. Sessions are not kept; a restore
logs everyone out. A database restored into is first waited for until its
real server takes connections (on an empty volume the image's init runs
a temporary one, then restarts it), `RESTORE_WAIT` seconds at most (180).
A database joins the archive as a name in `backup.sh`'s `dbs` and in
`restore.sh`'s list.

### Off the box

An archive on the box dies with the box. The copy that does not is pulled
by another machine (a laptop) over SSH: `offbox/pull.sh` there asks the
box's side, `offbox/box.sh` in this directory, for a fresh dump, then copies
every archive and every one of the store's bytes not yet there, each
checked against its sha256 (archives are listed with theirs, bytes are
named by it). Nothing is deleted on either side, so the copy is a superset
of every dump the box ever held; the box's own `backups/` is not pruned
either (watch its disk).

```
<dest>/dumps/lab-<time>.tar.gz   every archive, as the box named it
<dest>/bytes/<sha256>            the store's bytes
<dest>/last-good                 touched when a pull ended well
```

The store's bytes: lab runs no artifact_store yet (a picture the draw tool
makes is journaled in threads' database, so it rides the dump), and
`box.sh bytes` lists none. `LAB_BYTES_DIR`, a directory of files named by
sha256, is the seam the check uses; when the store joins, `bytes` and
`byte` read its bucket.

A failing copy is said, never silent, on both sides:

- **here** (the laptop): a pull older than a day (`LAB_PULL_STALE`, hours)
  makes `pull.sh` print `lab backup is stale: …`, exit 1 (the unit shows
  failed in `systemctl --user`), and raise a desktop notification
  (`notify-send`, when there is one);
- **on the box**: a good pull touches `backups/.pulled`;
  `./offbox/box.sh stale` prints `lab backup: nobody has pulled a copy off
  this box since …` and exits 1 when that is over a day old, or never
  happened. Run it from whatever watches the box (cron mailing its output,
  a health check); lab runs no notifier yet.

The pull runs on a systemd user timer on the laptop, hourly; it pulls only
when the last good pull is over `LAB_PULL_EVERY` hours old (20), so a
failing one is retried within the hour and a working one dumps once a day.
Installing it, on the laptop, in a units checkout:

```bash
cp stacks/lab/offbox/pull.env.example _ignored/lab-pull.env
$EDITOR _ignored/lab-pull.env          # LAB_PULL_SSH, LAB_PULL_LAB, LAB_PULL_DEST
ssh <ssh-alias-of-the-box> true        # a key, no prompt: the timer is unattended
stacks/lab/offbox/pull.sh --now        # one pull by hand, to see it work

mkdir -p ~/.config/systemd/user
sed "s|<units-checkout>|$PWD|" stacks/lab/offbox/lab-pull.service > ~/.config/systemd/user/lab-pull.service
cp stacks/lab/offbox/lab-pull.timer ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now lab-pull.timer
systemctl --user list-timers lab-pull.timer   # when it fires next
journalctl --user -u lab-pull.service         # what each pull said
```

`_ignored/` is gitignored, and `scripts/check_secrets.sh` looks for the
values of `_ignored/*.env*` in what is tracked. A restore from the copy is
`./restore.sh <dest>/dumps/<archive>` on any lab, with the archive copied
there first.

## Not here yet

- the chat keeps nothing: one conversation per page, gone on reload;

- tokens for programs: a machine calling the lab's API;
- mail to the outside world (UNIT-23);
- plain http on port 80 sending to https.
