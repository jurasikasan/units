# subjects — contract

The registry of subjects: who exists in a deployment, how they are addressed and
what they are. A subject is an identity — a person, an AI agent, a service —
not an account (that is auth's) and not a chat member (that is whatever keeps
the conversation). Who drives a subject right now (a model, a keyboard, a phone
call) is the runtime's business, not the registry's.

## 1. What it keeps

| Field | Meaning |
|---|---|
| `id` | uuid, minted on create |
| `name` | display name |
| `nick` | how people and models address the subject: `^[a-z][a-z0-9_-]{1,31}$`, unique across every subject ever created (deleted ones included), set once and never changed |
| `bio` | who the subject is, in free text |
| `subject_type` | what the subject is, in the deployment's own words (`human`, `ai`, …); required, never empty |
| `preferences` | a JSON object stored and returned as it came; its keys belong to whoever reads them |

The full shape is the swagger spec in `docs/`.

## 2. Routes

- `POST /subjects` — 201 with the subject; 400 on an empty type, preferences
  that are not an object, or a nick that breaks its rules; 409 when the nick is
  taken.
- `GET /subjects` — every subject not deleted, newest first.
- `GET /subjects/{id}` — 404 when there is none or it is deleted.
- `PUT /subjects/{id}` — replaces the subject: a field left out is written
  empty, `preferences` included. A nick in the body must equal the stored one.
- `DELETE /subjects/{id}` — soft delete; 404 when there is none. Which subjects
  a deployment must never lose is its own policy, held at its own door.

## 3. Boundaries

- Its database is its own, on a network only it joins; nobody else opens a
  socket to it. The schema is migrated on boot.
- It holds no rows of its own making: which subjects exist is the deployment's
  data. Each compose that needs subjects runs its own instance and seeds it.
- It publishes no events and has no MCP.
