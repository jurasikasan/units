# threads

The traces subjects leave in the world: threads as append-only journals. What
it promises is `CONTRACT.md`; this file is for working inside it.

- `internal/thread` — handler → service → repository; `validate.go` holds an
  entry to the contract's kinds and blocks.
- `internal/events` — the `threads_events` stream and its one type; the
  service publishes after the entry is stored, logs a failed publish and
  returns the entry anyway. No `REDIS_ADDR`, no publisher: threads works as
  before and announces nothing.
- `internal/db` — the migration runner and `migrations/`, applied on boot.
- `internal/thread/repository_test.go` — the SQL against a real Postgres
  (`THREADS_TEST_DATABASE_URL`); `check.sh` starts one in docker.
- `internal/thread/announce_test.go` — the event's shape, a Redis that is down
  (the append still 201), and the entry on a real stream
  (`THREADS_TEST_REDIS_ADDR`; `check.sh` starts one too).
- `cmd/app/routes_documented_test.go` — every registered route is in
  `docs/swagger.json` and back; after a handler change run `bash doc.sh`.
- `showcase/` — the service on its own Postgres and a Redis, driven by curl,
  with each append's event read off the stream:
  `docker compose -f showcase/compose.yaml up --build --attach demo --attach events --exit-code-from events events demo`.

```bash
bash check.sh
```
