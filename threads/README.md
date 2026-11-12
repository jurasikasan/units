# threads

The traces subjects leave in the world: threads as append-only journals. What
it promises is `CONTRACT.md`; this file is for working inside it.

- `internal/thread` — handler → service → repository; `validate.go` holds an
  entry to the contract's kinds and blocks.
- `internal/db` — the migration runner and `migrations/`, applied on boot.
- `internal/thread/repository_test.go` — the SQL against a real Postgres
  (`THREADS_TEST_DATABASE_URL`); `check.sh` starts one in docker.
- `cmd/app/routes_documented_test.go` — every registered route is in
  `docs/swagger.json` and back; after a handler change run `bash doc.sh`.
- `showcase/` — the service on its own Postgres, driven by curl:
  `docker compose -f showcase/compose.yaml up --build --abort-on-container-exit demo`.

```bash
bash check.sh
```
