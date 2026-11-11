# subjects

Who exists in a deployment: a registry of identities with a permanent nick, a
type and free-form preferences. What it promises is `CONTRACT.md`; this file is
for working inside it.

- `internal/subject` — handler → service → repository.
- `internal/db` — the migration runner and `migrations/`, applied on boot.
- `cmd/app/routes_documented_test.go` — every registered route is in
  `docs/swagger.json` and back; after a handler change run `bash doc.sh`.
- `showcase/` — the service on its own Postgres, driven by curl:
  `docker compose -f showcase/compose.yaml up --build --abort-on-container-exit demo`.

```bash
bash check.sh
```
