# reins

Where a principal holds the reins of its subjects: which subjects an account
at the door may look into and take up, the two personas a principal first
seen is given in `subjects`, and a steward's changes to a subject's name,
bio and preferences (whose keys are named in the contract), who holds each
subject's reins now (a principal on a device, a brain, or nobody), and the face's door
into the world: threads written into only as an avatar the principal steers,
and followed as one stream. What it promises is `CONTRACT.md`; this file is
for working inside it.

- `internal/stewardship` — handler → service → repository; `registry.go` is
  the client of `subjects` (a change reads the subject raw and PUTs it back
  whole, so fields reins does not know survive), `model.go` the personas a
  login is given and what a change may carry.
- `internal/world` — the face's door into the world: `journal.go` the
  client of `threads`, `service.go` every act only as an avatar the caller
  steers, `stream.go` `/me/stream` (threads' wake-ups read back, writing
  pieces relayed, the position as SSE's id). `world_test.go` holds the routes
  to fakes and the stream to a real Redis (`REINS_TEST_REDIS_ADDR`;
  `check.sh` starts one in docker).
- `internal/holds` — the one wheel: who holds each subject's reins now
  (`holds`, `presence`), take, give back, hand to a brain (the driver's
  profiles, `HTTPBrains`), stop, the lapse of a gone device's hold, every
  change on `reins_events`; the inner port's `GET /holds` for drivers, and
  `POST /holds/{id}/release` by which a brain lets go of its own hold.
  `holds_test.go` holds the SQL and the lapse against a real Postgres, in
  a schema of its own.
- `internal/db` — the migration runner and `migrations/`, applied on boot.
- `internal/stewardship/repository_test.go` — the SQL and the per-principal
  lock against a real Postgres (`REINS_TEST_DATABASE_URL`); `check.sh` starts
  one in docker.
- `cmd/app/routes_documented_test.go` — every registered route is in
  `docs/swagger.json` and back; after a handler change run `bash doc.sh`.
- `showcase/` — reins, `subjects` and `threads`, each on its own Postgres,
  and the bus, driven by curl with `X-Principal-Id` set as a door would (a
  thread opened, followed as a stream, written into as each avatar):
  `docker compose -f showcase/compose.yaml up --build --abort-on-container-exit demo`.

Config: `DATABASE_URL` (its own Postgres), `SUBJECTS_URL`, `PORT` (8087);
`THREADS_URL`, `REDIS_ADDR` and `WRITING_STREAMS` for the world (without
them its routes answer 503); `INTERNAL_PORT` (8088), `HOLD_GRACE` (2m),
`DEFAULT_BRAIN` and `BRAINS_URL` for the holds.

```bash
bash check.sh
```
