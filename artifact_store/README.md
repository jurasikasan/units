# artifact_store

Named files in spaces: put, get, list, delete, and a read of the bytes by
their sha256. Identical bytes are kept once; a space can expire what nobody
wrote for a while. What it promises is
`CONTRACT.md`; this file is for working inside it.

- `internal/store` — handler → service; `dedup.go` keeps names in Postgres and
  bytes in MinIO under their sha256, `sweep.go` removes unnamed bytes,
  `retention.go` reads `RETENTION`.
- `internal/objects` — the way to MinIO (any S3).
- `internal/store/minio_store_test.go` — against a real Postgres and MinIO
  (`STORE_TEST_DATABASE_URL`, `STORE_TEST_MINIO_ENDPOINT`); `check.sh` starts
  both in docker.
- `internal/app/routes_documented_test.go` — every registered route is in
  `docs/swagger.yaml` and back; after a handler change run `bash doc.sh`.
- `showcase/` — the service on its own Postgres and MinIO, driven by curl:
  `docker compose -f showcase/compose.yaml up --build --abort-on-container-exit demo`.

```bash
bash check.sh
```
