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
- `POST /subjects/{id}/handover {"to": "<principal>"}` — 204: the caller stops
  steering the subject and `to` starts. 403 when the caller does not steer it;
  400 when `to` is empty or the caller. Nothing is written to `subjects` or
  `threads`.

The full shapes are the swagger spec in `docs/`.

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
