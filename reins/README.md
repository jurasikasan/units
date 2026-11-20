# reins

Where a principal holds the reins of its subjects: which subjects an account
at the door may look into and take up, the two personas a principal first
seen is given in `subjects`, and a steward's changes to a subject's name,
bio and preferences (whose keys are named in the contract). What it promises is `CONTRACT.md`; this file is
for working inside it.

- `internal/stewardship` — handler → service → repository; `registry.go` is
  the client of `subjects` (a change reads the subject raw and PUTs it back
  whole, so fields reins does not know survive), `model.go` the personas a
  login is given and what a change may carry.
- `internal/db` — the migration runner and `migrations/`, applied on boot.
- `internal/stewardship/repository_test.go` — the SQL and the per-principal
  lock against a real Postgres (`REINS_TEST_DATABASE_URL`); `check.sh` starts
  one in docker.
- `cmd/app/routes_documented_test.go` — every registered route is in
  `docs/swagger.json` and back; after a handler change run `bash doc.sh`.
- `showcase/` — reins and `subjects`, each on its own Postgres, driven by curl
  with `X-Principal-Id` set as a door would:
  `docker compose -f showcase/compose.yaml up --build --abort-on-container-exit demo`.

Config: `DATABASE_URL` (its own Postgres), `SUBJECTS_URL`, `PORT` (8087).

```bash
bash check.sh
```
