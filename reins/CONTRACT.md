# reins — contract

Where a principal holds the reins of its subjects. A **principal** is an
account at the door (`X-Principal-Id`, as the door sets it); a **subject** is a
character in `subjects`, whoever drives it. **Stewardship** is which principal
may look into a subject and take it up, as actor or watcher: a relation out of
the world, invisible to the subjects. Handing a subject over changes who sees
it from inside; nothing in the world changes.

reins is the face's door into the world. It starts with stewardship; keeping
the conversation (writing the occupied subject's acts into `threads`, pushing
what changes to the face) is meant to grow here.

Not here: membership in communities (hired, fired) is an in-world fact and
belongs to a community's journal in `threads`.

## 1. What it keeps

| Table | Meaning |
|---|---|
| `stewardship(principal_id, subject_id, since)` | principal ↔ subject, many-to-many; a principal may steer many subjects, a subject may have many stewards |
| `principals(principal_id, first_seen, settled_at)` | principals met, and when their first personas were all made |

The subjects themselves live in `subjects`; reins keeps only their ids.

## 2. Routes

Each needs `X-Principal-Id`; without it, 401. reins trusts the header and is
reached only through the door.

- `GET /me/subjects` — the subjects the caller steers, as `subjects` returns
  them, oldest stewardship first; deleted ones left out.
  The first call for a principal makes its two personas in `subjects`: nick
  `<login>` (subject_type `human`) and `<login>-assistant` (`assistant`),
  names empty, and records that the principal steers both.
  - 409 when a persona's nick is taken by another subject. It is never adopted:
    that would hand someone's avatar to whoever logs in with a matching name.
  - 422 when the login cannot become both nicks (`subjects`' rules: 2–32
    characters of `a-z 0-9 - _`, starting with a letter; so a `.`, capitals,
    or a login over 22 characters).
  - Both are logged with the reason, and the body says it.
  - 502 when `subjects` or the database does not answer.
- `GET /subjects/{id}` — one subject the caller steers, as `subjects`
  returns it. 403 when the caller does not steer it; 404 when it does but
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

### Preferences

A subject's `preferences` is a JSON object `subjects` keeps without looking
in; its keys belong to whoever reads them. These are the ones read now, each
the `id` of a model the agent lists at OpenAI's `GET /v1/models` (a voice:
one of that speech model's `voices`). A key left out means the agent's own
choice.

The same keys on every subject: they are its body, whoever is in it. Which
model thinks for a subject is not among them: that is the brain holding its
reins now (the page's pick, later a driver's), not a property of the body.

| Key | Read by | For |
|---|---|---|
| `ear` | the face: the mic's transcription `model`, a call's `audio.input.transcription.model` | the speech-to-text model its words are heard with |
| `mouth` | the face: read-aloud's `model`; a call's voice | the speech model it speaks with |
| `voice` | the face: read-aloud's `voice`; a call's `audio.output.voice` as `<mouth>_<voice>` | one of the mouth's voices |

Which subject's body feeds which request is the face's wiring for the classic
chat until the conversation is kept here: the person speaks as the subject
named by its login and hears the other one. reins does not check the values:
which models exist is the agent's to say.

## 3. Provisioning

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

## 4. Boundaries

- Its database is its own, on a network only it joins; nobody else opens a
  socket to it. The schema is migrated on boot.
- It holds no rows of its own making: which principal steers what is the
  deployment's data.
- It publishes no events and has no MCP.
- Of a subject it changes only what a steward may: name, bio, preferences.
  Its nick and type are `subjects`' rules; who drives it is not written there.
