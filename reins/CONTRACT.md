# reins — contract

Where a principal holds the reins of its subjects. A **principal** is an
account at the door (`X-Principal-Id`, as the door sets it); a **subject** is a
character in `subjects`, whoever drives it. **Stewardship** is which principal
may look into a subject and take it up, as actor or watcher: a relation out of
the world, invisible to the subjects. Handing a subject over changes who sees
it from inside; nothing in the world changes.

reins also keeps **who holds** each subject's reins now (section 5): the
one wheel. reins is the face's door into the world: a principal acts there
only as an avatar it steers and holds, written into `threads` as that
subject, and follows the threads its avatars are in as one stream (section
3). It keeps nothing of the world: the journal is `threads`'.

Not here: membership in communities (hired, fired) is an in-world fact and
belongs to a community's journal in `threads`.

## 1. What it keeps

| Table | Meaning |
|---|---|
| `stewardship(principal_id, subject_id, since)` | principal ↔ subject, many-to-many; a principal may steer many subjects, a subject may have many stewards |
| `principals(principal_id, first_seen, settled_at)` | principals met, and when their first personas were all made |
| `holds(subject_id, holder, device, since)` | who holds a subject's reins now, at most one: `principal:<id>` from a device, or `brain:<profile>`; no row, nobody (section 5) |
| `presence(principal_id, device, seen_at)` | when a principal's device was last seen, its stream open |

The subjects themselves live in `subjects`; reins keeps only their ids.

## 2. Routes

Each needs `X-Principal-Id`; without it, 401. reins trusts the header and is
reached only through the door.

- `GET /me/subjects` — the subjects the caller steers, as `subjects` returns
  them, oldest stewardship first; deleted ones left out.
  The first call for a principal makes its two personas in `subjects`: nick
  `<login>` and `<login>-assistant`, names empty, and records that the principal steers both.
  - 409 when a persona's nick is taken by another subject. It is never adopted:
    that would hand someone's avatar to whoever logs in with a matching name.
  - 422 when the login cannot become both nicks (`subjects`' rules: 2–32
    characters of `a-z 0-9 - _`, starting with a letter; so a `.`, capitals,
    or a login over 22 characters).
  - Both are logged with the reason, and the body says it.
  - 502 when `subjects` or the database does not answer.
  Each with its `current_brain` (section 5), as the caller sees it from
  its device (`X-Device-Id`).
- `GET /subjects/{id}` — one subject the caller steers, as `subjects`
  returns it, with its `current_brain`. 403 when the caller does not steer it; 404 when it does but
  `subjects` has it no more; 502 when `subjects` or the database does not
  answer.
- `PATCH /subjects/{id} {"name"?, "bio"?, "preferences"?}` — 200 with the
  subject as changed. A field left out is kept. Preferences are merged key by
  key into the stored ones: a key given replaces it, a key given as `null`
  takes it out, the rest are kept, so one reader's keys survive another's
  change. Written to `subjects` with its `PUT`, which replaces a subject
  whole, so reins sends back every field as it was stored (the nick, the
  type, any field reins does not know) with only these three changed. The
  nick never changes: a body naming it, or any other field, is 400, as are
  preferences that are not an object. 403, 404 and 502 as above. Read, then
  written: two changes at once may lose one, the last write winning.
- `POST /subjects/{id}/handover {"to": "<principal>"}` — 204: the caller stops
  steering the subject and `to` starts. 403 when the caller does not steer it;
  400 when `to` is empty or the caller. Nothing is written to `subjects` or
  `threads`.

The full shapes are the swagger spec in `docs/`.

### The world

Each act goes through an avatar the caller steers and holds from the
device it names (`X-Device-Id`; section 5), and is written into `threads`
as that subject. A steward who does not hold it watches: it reads, and an
act is 409 `held by <holder>` ("alice", "you on another device", "brain
fake", "nobody: take the reins first"). Without `THREADS_URL` these routes
answer 503.

- `GET /me/threads` — every thread where an avatar the caller steers is a
  member (`threads`' `?member=`), each once, as `threads` reads it: `name`
  and `members`. Newest first.
- `POST /me/threads {"as": "<avatar>", "with": ["<avatar>", …]}` — 201 with
  the new thread, opened by `as` (`opened_by`); `as` joins it, then each of
  `with`, each by a `join` entry sent by that subject. For now each of them
  must be an avatar the caller steers, else 403 and nothing is opened:
  putting in someone else's is an invitation, a later task. Opening is an
  act of `as`: 409 when the caller does not hold it. 400 without `as`.
- `POST /threads/{id}/entries {"as": "<avatar>", "content": [blocks]}` —
  201 with the entry as `threads` stored it: a `create` sent by `as`, its
  content `threads`' blocks (text, a picture as `image`, a document as
  `file`, …; `threads`' CONTRACT.md). 403 when the caller does not steer
  `as` or `as` is not a member; 404 when there is no such thread; 400 with
  `threads`' own words when it refuses the content; 413 over 16 MB (a
  picture or a file goes inline as a `data:` URL until its bytes are kept
  elsewhere).
- `GET /threads/{id}/entries` — as `threads` answers it (`after`, `before`,
  `tail`, `limit`), for a thread one of the caller's avatars is in, else
  403. Every entry, whoever sent it: a watcher sees the channel itself, not
  an avatar's projection.
- `GET /me/stream` — section 3.

### Preferences

A subject's `preferences` is a JSON object `subjects` keeps without looking
in; its keys belong to whoever reads them. These are the ones read now, each
the `id` of a model the agent lists at OpenAI's `GET /v1/models` (a voice:
one of that speech model's `voices`). A key left out means the agent's own
choice.

The same keys on every subject: they are its body, whoever is in it. Which
model thinks for a subject is not among them: that is the brain holding its
reins now (section 5), not a property of the body.

| Key | Read by | For |
|---|---|---|
| `ear` | the face: the mic's transcription `model`, a call's `audio.input.transcription.model` | the speech-to-text model its words are heard with |
| `mouth` | the face: read-aloud's `model`; a call's voice | the speech model it speaks with |
| `voice` | the face: read-aloud's `voice`; a call's `audio.output.voice` as `<mouth>_<voice>` | one of the mouth's voices |

Which subject's body feeds which request is the face's wiring for the classic
chat until the conversation is kept here: the person speaks as the subject
named by its login and hears the other one. reins does not check the values:
which models exist is the agent's to say.

## 3. The stream

`GET /me/stream` is one principal's view of the world as it changes, as
server-sent events. Without `REDIS_ADDR` it answers 503.

| `event` | `id` | `data` |
|---|---|---|
| `ready` | the position the stream starts after | `{"position"}` |
| `entry` | the wake-up's id on `threads_events` | one entry, as `threads` stores it |
| `writing` | none | a piece of a reply being written: its event's `data`, with its CloudEvents `type` beside |
| `hold` | none | who holds one of the principal's avatars now: `{subject, kind, principal?, profile?, you?, here?}`, `current_brain` as this principal sees it from the stream's device |
| `reset` | none | `{"reason"}` |

- **Entries.** On each `threads.entry.appended` on `threads_events` (the bus
  `threads` announces on, `REDIS_ADDR`) for a thread one of the principal's
  avatars is in, the entry is read back from `threads` and sent: the event
  only wakes reins, the journal is the truth. Who is in a thread is asked
  of `threads` once per stream, and again after a `join` or `leave` in it;
  which avatars the principal steers, every 30 s.
- **The position** is SSE's own: each `entry`'s id is its event's id on
  `threads_events`, so a reconnect with `Last-Event-ID` (an `EventSource`
  sends it by itself) resumes after it, as Matrix's `/sync` does with
  `since`; `?since=` says the same where the header cannot be set. No
  position: from now. A position the stream no longer holds (trimmed away,
  or past its end: a bus that came back empty) is a `reset`, then the
  stream goes on from now: the reader reloads what it shows from `threads`.
  A reader that opens the stream first and reads the threads after misses
  nothing in between; an entry it has twice it knows by its `seq`.
- **Writing.** A reply being written is ephemeral: its pieces are published
  by whoever writes it on its own stream (`WRITING_STREAMS`, by default
  `driver_events`, from `drivers/model`), and relayed here as `writing`
  for a thread the principal sees (its `data` names the `thread`), with no
  id: they are never journaled, and a reconnect does not bring them back.
  Only the finished reply is an `entry`.
- **Holds.** Each `reins.hold.changed` on `reins_events` for an avatar
  the principal steers is sent as `hold`, with no id: who holds what is
  read again with the subjects. A former holder learns here that its
  reins were taken.
- **The device.** `?device=` names the device the stream is open on (an
  `EventSource` sets no header). While it is open, the device is seen
  (every 20 s); a principal's hold from a device lapses when it has been
  gone longer than `HOLD_GRACE` (section 5).
- A failure to read the bus or `threads` ends the stream before the event
  it was on; the reader comes back from its last id and it is tried again.
  A comment (`: keep-alive`) goes every 15 s when nothing else does.

## 4. Provisioning

The personas made, and before the principal is settled, it is welcomed:
`<login>-assistant` is handed to the default brain (`DEFAULT_BRAIN`, when
set and nobody holds it yet), and the first thread is opened: opened by
the assistant, which joins it, then `<login>`. Nothing is said in it: the
brain in the assistant speaks first (`drivers/model` greets in a thread
its subject opened where nothing is said). Once only: a thread the
assistant opened already is enough. `<login>` is held by nobody until the
face takes it up. The human/assistant pair is the starting case, not a
type: both are characters like any other.

- A principal is provisioned once. Until every persona is made and recorded,
  each visit completes what is missing; then the principal is settled and
  nothing is made again — a persona handed to someone else is not made anew.
- Serialised per principal (a Postgres advisory lock), and checked again under
  the lock, so concurrent first visits make each persona once.
- A wanted nick counts as there only when the principal already steers a
  subject with it. Each subject is recorded as soon as `subjects` made it,
  outside any transaction, so a run cut half way keeps what it made.
- Known gap: a run cut between `subjects`' 201 and the record leaves that nick
  held by a subject nobody steers, and the principal is refused 409 from then
  on. `subjects` mints the ids, so reins cannot record first; an operator
  records the stewardship by hand.

## 5. Who holds the reins: the one wheel

Each subject has at most one holder at a time, and writing as it needs its
reins:

- **a principal, from a device**: `principal:<id>` and the device's id
  (the face makes one per browser and keeps it; a phone could be one, the
  shape allows it). One device may hold several avatars at once;
- **a brain**: `brain:<profile>`, a driver's profile (who moved into the
  avatar and thinks for it; `drivers/model`'s `PROFILES`);
- **nobody**: the avatar is offline.

Only the holder is someone to tell about the avatar's events; an offline
avatar has nowhere to send to. Who holds a subject is shown to principals
(`current_brain`), never to characters, and never written into
`subjects`.

`current_brain`: `{kind, principal?, profile?, you?, here?, since?}` —
`kind` `principal` (`you`: the caller; `here`: from the device asking),
`brain` (with its `profile`) or `offline`.

Routes, each needing `X-Principal-Id`; any steward of the subject may
(stewards are equal: taking from anyone is the kill switch). 403 when the
caller does not steer it.

- `POST /subjects/{id}/take` — the caller holds it from its device
  (`X-Device-Id`, else `?device=`; 400 without). 200 with `current_brain`.
- `POST /subjects/{id}/give-back` — nobody holds it. 204.
- `POST /subjects/{id}/hand {"profile"}` — a brain holds it: one of the
  profiles `GET /brains` lists, else 422. 200 with `current_brain`.
- `GET /brains` — the profiles there are, names only, as the driver says
  them (`BRAINS_URL`'s `GET /profiles`); 503 without `BRAINS_URL`.
- `POST /threads/{id}/stop {"subject"}` — stop the reply the subject's
  brain is writing in the thread: an action on the brain, not on a page's
  request. Published as `reins.reply.stop`; the driver cancels the run and
  journals what was written so far when there is any text, nothing when
  there is none. 202; 503 without a bus.

**Lapse.** A principal's hold lapses when its device has been gone longer
than `HOLD_GRACE` (2 min), counted from the later of the device last seen
(its stream open, section 3) and the take; then nobody holds the avatar.
A brain's hold does not lapse. Checked every quarter of the grace (at most
15 s).

**Events.** Every change of holder is published on `reins_events`
([contracts/events.md](../contracts/events.md), source `/reins`, `subject`
the subject's id), capped (about 10 000):

| `type` | `data` |
|---|---|
| `reins.hold.changed` | `subject`, `holder` (`principal:<id>`, `brain:<profile>`, or `""` for nobody), `device` (a principal's) |
| `reins.reply.stop` | `thread`, `subject`, `by` (the principal) |

The event only wakes: a driver reads the truth back, a stream shows it.

**The inner port** (`INTERNAL_PORT`, 8088), never forwarded by the door:
`GET /holds` is every hold of a brain, `[{subject, holder, profile,
since}]`: a driver reads it at boot and on each `reins.hold.changed`, and
keeps those of its own profiles.

## 6. Boundaries

- Its database is its own, on a network only it joins; nobody else opens a
  socket to it. The schema is migrated on boot.
- It holds no rows of its own making: which principal steers what is the
  deployment's data. Who holds what starts from provisioning and the
  principals' acts.
- It publishes `reins_events` (section 5), and has no MCP. It consumes
  `threads_events`, the writing streams and its own `reins_events`, each
  stream on its own, from now or the reader's position: no consumer group,
  nothing acknowledged.
- Of a subject it changes only what a steward may: name, bio, preferences.
  Its nick is `subjects`' rule; who drives it is not written there.
