# body — contract

Revision 3 (UNIT-122): one process carries many bodies, made, changed and let go through an
API (§2.6); each lives under `/bodies/<name>/`. A body named in the env is made at start as
before.
Revision 2 (UNIT-127): its stream over HTTP for a head outside the inside network (§2.5).
Revision 1 (UNIT-119): the body, a socket between the world and a head. It sees, hears,
and does what it is given — today two things: speak aloud, and skip the turn.

```
senses' streams ─(its own sessions only)─▶ body's stream ◀─ its acts
        ▲                                                     │
  hearing / sight open                       MCP tools for a head
  (its ears, its eyes: GET /body)            speak_aloud → speaking → the ears in earshot
                                             GET /bodies/<name>/events: the stream, over HTTP
```

A head is whoever drives it — a model through `brain/`, Claude as a puppet, a person
through a page — and the body knows none of them: the same MCP tools and the same stream
for all. Tools are the body's extensions (a phone, a chat, a sword are, for simplicity,
the body too); *how* an act is done is the body's own, automatic, not the head's thinking.

Each body is one subject: its `id`. A process — the host (§2.6) — carries as many as are
declared; each has the walls below, and §2 names exactly what crosses them; nothing else
does. What the env names (`BODY_*`) is the one body made from the env; a spec says the same
for a body made through the API.

## 1. What it is

- **Self.** What an observer perceives of this subject, `BODY_DESCRIPTION`, served with
  its id, name and where its senses take input (§2.4). The world will later hand this to
  other bodies' senses; nothing reads it for perception yet.
- **Senses.** On start the body opens one hearing session and one sight session, each
  only when its URL is configured. What they answer — `audio_in`, `video_in`/`sight_in` —
  are its ears and eyes: whatever sends there is heard or seen. In "nowhere" that is
  another body's mouth; later, the world. A session that closes by itself (idle, failure)
  is opened again; a sense that will not open is asked again every `BODY_RETRY`. On
  shutdown the body closes its sessions.
- **Its own perception, and only its own.** Of the senses' streams the body keeps the
  events whose `ce_subject` is one of its sessions and republishes them onto its stream.
  A head reads only that stream: it never sees another body's perception.
- **Acts**, as MCP tools (§2.3). The list is the body's to declare; a head lists it.
- **What it did** goes onto the same stream, so a head and an observer see its acts
  beside its percepts.

## 2. Walls

### 2.1 Senses: hearing and sight

MCP over streamable HTTP (the official Go SDK) at `HEARING_MCP_URL`, `SIGHT_MCP_URL`.

| Call | Arguments sent | Read from the answer |
|---|---|---|
| hearing `open` | `speech_language` = `BODY_LANGUAGE`, when set | `session_id`, `audio_in` |
| sight `open` | `language` = `BODY_LANGUAGE`, when set | `session_id`, `video_in`, `sight_in` |
| either `close` | `session_id` | nothing (on shutdown) |

Their streams, `hearing_events` and `sight_events`, read once for every body of the host
by its consumer group `BODY_GROUP` from the host's start (a new group starts at the
stream's end), each entry handed to the body whose session it is about. Read from an
entry: `ce_subject`, `ce_type`; and to republish, `ce_id`, `ce_time`, `ce_traceparent`,
`data` as they are. Every entry is acknowledged once looked at.

- An entry whose `ce_subject` is no body's session here: passed over, counted (the host's
  `foreign`).
- One of its sessions', of a type in the table below: republished (§2.2).
- One of its sessions', of any other type: counted (`dropped`), not republished.
- `*.session-closed` of its session: republished, then the session is forgotten and its
  sense opened again; the new address replaces the old in `GET /body`.

| Republished `ce_type` |
|---|
| `speechrecog.start-of-input`, `speechrecog.recognition-complete`, `speechrecog.session-closed` |
| `sightrecog.start-of-input`, `sightrecog.recognition-complete`, `sightrecog.session-closed` |

The body reads nothing in `data`: what a sense recognised is the sense's to say and the
head's to understand.

### 2.2 Out: the body's stream

`BODY_STREAM` (default `body_<name>_events`), the envelope of
[`contracts/events.md`](../contracts/events.md), `ce_subject` = `BODY_ID` on every entry.

**Percepts**, republished: `ce_id`, `ce_type`, `ce_time`, `ce_traceparent` and `data`
exactly as the sense wrote them — perception times and transport time unchanged —;
`ce_source` = `/bodies/<name>/hearing` or `/bodies/<name>/sight`, so a consumer that
names a channel by the source's last segment (the brain) sees `hearing` and `sight`.

**Acts**: `ce_type` `body.act-complete`, `ce_source` `/bodies/<name>`, one per tool call
that ran (§2.3), when it ended; `ce_traceparent` the call's, when it came with one.

```json
{"tool": "speak_aloud", "args": {"text": "hello, who is there?"},
 "result": {"spoken": "hello, who is there?", "completion_cause": "normal", "listeners": 1},
 "is_error": false, "started_at": "2026-11-27T10:15:02.310Z", "ended_at": "2026-11-27T10:15:04.020Z"}
```

A failed act: `is_error: true`, `result: null`, `error` the tool error's text.

### 2.3 In: the head's MCP

Streamable HTTP, stateless, JSON responses, on `/mcp`. Tool errors are MCP tool errors
whose text starts with a code (`bad_argument`, `speaking`, `timeout`). One act runs at a
time: a second call waits for the first (one body, one mouth). A call's `_meta.traceparent`
is passed on to speaking and onto the act's event.

**`speak_aloud(text)`** → `{spoken, completion_cause?, listeners}` — only on a body with a
mouth (`SPEAKING_MCP_URL`). The body says it to whoever is within earshot:

1. For each entry in its earshot, the listener's self (§2.4) for its `ears[0].audio_in` now:
   an entry `…/bodies/<name>` (a body on a host) is read as it is, `GET {entry}`; a bare base
   URL (a body run alone, by env) at `GET {entry}/body`. A body that does not answer, or has no ear open, does not hear this phrase.
2. One speaking session per listener, aimed at its ear: `open {to: audio_in, voice_name:
   BODY_VOICE}` once, kept while the ear stays where it was; when the ear has moved, the
   old session is closed and a new one opened. A session speaking closed on its own
   (`speechsynth.session-closed` on its stream, or `speak` answering `unknown_session`) is
   opened again, once per phrase.
3. `speak {session_id, text}` to each, then wait for each phrase's
   `speechsynth.speak-complete` (`speech_events`, the same group; `request_id`, `spoken`,
   `completion_cause` read) — at most `BODY_SPEAK_TIMEOUT` for all of them.

`spoken` is the most of the text that went out to any listener, `completion_cause`
speaking's for that phrase (`normal`: all of it); `listeners` how many ears it was sent
to. Nobody in earshot is not an error: `{spoken: "", listeners: 0}`, nothing opened. Every
listener's speaking failing is `speaking: …`; no phrase reported in time is `timeout: …`.

What speaking reports is what it *sent*, not what anyone heard: whether a listener heard
it is that listener's own perception, on its own stream.

**`skip_turn()`** → `{skipped: true}`. Does nothing, on purpose: the head waits for the
next batch of perception. Answers at once.

**Resource `body://self`** — `application/json`, the same as `GET /body`.

### 2.4 `GET /bodies/<name>` (and `GET /body`)

```json
{"id": "0f6b…", "name": "a", "description": "a tall woman in a grey coat",
 "ears": [{"audio_in": "hearing:44017"}],
 "eyes": [{"video_in": "sight:45000", "sight_in": "http://sight:8080/in/sg-7d02c1aa"}]}
```

The body as an observer perceives it and where its senses take input. The env's body
answers at `GET /body` too, as a body run alone always did.

`ears` and `eyes` are where its senses take input now: empty while a sense is not open (or
not configured). RTP sent to an ear is heard in the formats hearing takes (speaking's L16
among them). Every mouth that sends there is heard, each as a source of its own (hearing
§4: its `source` in the events), so the bodies in each other's earshot share one ear
each, and a mouth that reopens is heard again.

### 2.5 `GET /bodies/<name>/events`

The body's stream (§2.2) as [server-sent events](https://html.spec.whatwg.org/multipage/server-sent-events.html),
for a head that is not on the inside network: with this and `/mcp` a head needs only the
body's URL. `<name>` is a body on this host; any other name is `404`.

- **Where it starts.** After the entry `Last-Event-ID` names (a reconnect), else after
  `?from=<id>` (`from=0`: the whole stream), else from now — after the entry that was
  last when the request came. An id that is not a stream entry id is `400`.
- **One entry, one event.** `id:` the Redis entry id, `data:` the entry as a CloudEvent in
  its JSON format: each `ce_` field without the prefix, `data` as JSON (as a string when it
  is not JSON). No `event:` line: an `EventSource` gets each in `onmessage`.

```
id: 1764238502310-0
data: {"specversion":"1.0","id":"7f1c…","source":"/bodies/b/hearing","type":"speechrecog.recognition-complete","subject":"0f6b…","time":"2026-11-27T10:15:04.120Z","datacontenttype":"application/json","traceparent":"00-…","data":{"text":"Hello, who is there?",…}}
```

- A comment line (`: keep-alive`) when nothing came for 15 s; the stream ends only when
  the head goes or Redis fails (counted in `errors`; the head reconnects with its last id).
- Each watcher holds one blocking read on Redis; there is no fan-out of its own.

`GET /health`: `{"status": "ok", "version", "bodies": {"<name>": counts}, "foreign",
"errors"}` — a body's counts: `relayed`, `dropped`, `opened`, `open_errors`, `acts`,
`act_errors`, `errors`; `foreign` and `errors` at the top are the host's reader's. For the
env's body also `self` and its `counts` (with the host's `foreign` and `errors` in them), as
a body run alone always answered.

### 2.6 The host: `/bodies`

One process carries many bodies. Declarative: the application says what a body is and the
host makes it so; the host keeps nothing — after a restart it carries only what is declared
again (a body's stream stays in Redis). The units a body is made of are the host's
(`HEARING_MCP_URL`, `SIGHT_MCP_URL`, `SPEAKING_MCP_URL`); a spec names which it has.

**`PUT /bodies/{name}`** — a spec, JSON, unknown fields refused. `{name}`: lower-case
letters, digits, `_`, `-`; it names the stream `body_<name>_events` and the source.

```json
{"id": "0f6b…", "description": "a tall woman in a grey coat", "senses": ["hearing", "sight"],
 "voice": "piper_lessac", "language": "en", "earshot": ["http://bodies:8080/bodies/b"]}
```

| Field | Meaning | Absent |
|---|---|---|
| `id` | the subject's uuid | derived: uuid v5 (URL namespace) of `urn:units:body:<name>` |
| `description` | what an observer perceives of it | empty |
| `senses` | `hearing`, `sight`: each a session opened on the host's sense; one the host has not is refused | none |
| `voice` | speaking's `voice_name`: a body with a voice has a mouth and `speak_aloud`; refused when the host has no speaking | no mouth |
| `language` | the senses' session language, ISO 639-1 | they guess |
| `earshot` | the bodies that hear it: `…/bodies/<name>` URLs, or a body run alone by its base URL (§2.3) | nobody |

`201` with the body when it is new. The same spec again: `200`, nothing changes. Another
spec: the body is let go and made anew, `200` — its sessions closed and opened again, its
stream the same. A spec refused: `400`, `bad_spec: …`.

The answer, and each item of `GET /bodies` (by name):

```json
{"name": "a", "id": "0f6b…", "spec": {…},
 "body": "/bodies/a", "mcp": "/bodies/a/mcp", "events": "/bodies/a/events"}
```

Its addresses are relative to the host. Under them: its self (§2.4), its MCP (§2.3, the
same tools and resource), its stream (§2.5).

**`DELETE /bodies/{name}`** — lets it go: its senses' sessions and its mouths closed; `204`,
or `404`.

The env's body (`BODY_NAME`) is made by the same `PUT` at start, its MCP also at `/mcp`.

## 3. The log

JSON lines on stdout: `sense: opened` (sense, session, address), `sense: its session
closed, opening again`, `mouth: aimed` / `spoken` per listener, `act` (tool, args, result,
is_error, act_ms), and every failure with its `err`.

## 4. What it does not do

- think, or know there is a head: it waits to be told;
- read what its senses recognised: it carries it;
- know devices: in "nowhere" there are none, and the body is described by words alone;
- hear itself: its mouth is aimed only at the ears in its earshot.

## 5. Config

| Variable | Meaning | Default |
|---|---|---|
| `REDIS_ADDR` | the streams | — (required) |
| `BODY_NAME` | a body made from the env at start, by this name: lower-case letters, digits, `_`, `-`; names its stream and source | — (no body from the env: only what is PUT) |
| `BODY_ID` | the subject's uuid | derived: uuid v5 (URL namespace) of `urn:units:body:<name>` |
| `BODY_DESCRIPTION` / `BODY_DESCRIPTION_FILE` | what an observer perceives of it; text, or a file — not both | empty |
| `BODY_STREAM` | where it publishes | `body_<name>_events` |
| `BODY_GROUP` | the host's consumer group on the senses' and speaking's streams; one per running host | `body_<name>` with `BODY_NAME`, else `bodies` |
| `HEARING_MCP_URL` | hearing's MCP, for every body here | — (no ears) |
| `SIGHT_MCP_URL` | sight's MCP, for every body here | — (no eyes) |
| `SPEAKING_MCP_URL` | speaking's MCP, for every body here | — (no mouth, no `speak_aloud`) |
| `BODY_VOICE` | speaking's `voice_name` | — (required with a mouth) |
| `BODY_LANGUAGE` | the senses' session language, ISO 639-1 | — (they guess) |
| `BODY_EARSHOT` | the bodies that hear it, comma-separated: `…/bodies/<name>` URLs, or bare base URLs of bodies run alone | — (nobody) |
| `BODY_SPEAK_TIMEOUT` | how long `speak_aloud` waits for its phrase, for every body here | `60s` |
| `BODY_RETRY` | the pause before a sense is asked to open again | `2s` |
| `PORT` | `/bodies`, each body's MCP, self and events, `/mcp` and `GET /body` for the env's, `GET /health` | `8080` |

`BODY_ID`, `BODY_DESCRIPTION(_FILE)`, `BODY_STREAM`, `BODY_VOICE`, `BODY_LANGUAGE` and
`BODY_EARSHOT` make the env's body only; the rest are the host's. A malformed value stops
the start.

## 6. Open

- **The world** hands bodies to each other's senses: who is in earshot or in sight,
  and what an observer perceives of them (`description`), instead of earshot lists.
- **A changed spec in place** — today another spec makes the body anew (its sessions
  reopened), acceptable while bodies are few.
- **More acts**: the tools that are the body's extensions (a phone, a chat).
- **Devices** described in `GET /body` once there are any.
- **A sense lost without a word** (it restarted) is not noticed until it sends one;
  the ear then points nowhere.
