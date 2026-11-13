# artifact_store — contract

Status: live, 2026-10-24.

## 1. What it is

A store of named files. A file lives in a space (`docs`, `archive`) under a readable name
(`R-412.result.bundle`). Writing under the same name replaces the file. Identical
bytes are kept once, however many names point to them. A space can have a rule that
removes files nobody has written for a while. The store does not know what a space or
a name stands for.

## 2. Depends on

Manifest: `unit.yaml`. Environment:

| Variable | Required | Meaning |
|---|---|---|
| `DATABASE_URL` | yes | its own Postgres: which name points to which bytes; schema migrated on start |
| `MINIO_ENDPOINT`, `MINIO_ACCESS_KEY_ID`, `MINIO_SECRET_ACCESS_KEY`, `MINIO_USE_SSL` | yes | where the bytes are kept |
| `MINIO_BUCKET` | no, default `artifact-store` | created on first upload |
| `RETENTION` | no | rules per space, section 5; without it nothing expires |
| `STORE_SWEEP_PERIOD` | no, default `1h` | how often expiry and the removal of unnamed bytes run (Go duration: `10m`, `1h`); a malformed value stops the start |
| `PORT` | no, default `8094` | |

## 3. Interface

HTTP. The contract for the routes is [`docs/swagger.yaml`](docs/swagger.yaml): the
methods, the paths, what each route returns, and the status of each answer.

What the spec cannot say:

- **PUT replaces.** A second PUT under the same name makes the new bytes the file. It
  is safe to repeat.
- **PUT is all or nothing.** If the upload breaks, the previous file (or no file) stays.
  A reader never gets half a file.
- `created_by` is the `X-Principal-Id` header as received, or `null`. Whoever stands in
  front of the store vouches for it.

## 4. Identifiers

- `space`: `^[a-z][a-z0-9-]{2,31}$`. No need to create a space before writing to it.
- `name`: `^[A-Za-z0-9_][A-Za-z0-9._-]{0,254}$`. Readable, never a path: no `/`. A
  consumer may write it into a folder as is.
- The store assigns nothing. Keeping names unique for different things is the
  consumer's job. A consumer that starts its names with its own key
  (`R-412.result.bundle`) lists one key's files with `?prefix=R-412.`.

## 5. Guarantees and limits

- One file is at most 512 MiB. A larger body is refused with 413 before anything is
  stored.
- **Deduplication:** identical bytes are stored once. The only visible sign is equal
  `sha256`. Bytes no name points to any more are removed in the background.
- **Expiry:** `RETENTION="docs=90d,archive=never"`. A file not written for that long is
  removed. A space not listed never expires. A malformed value stops the start with a
  message that names it.
- **When the cleanup runs:** expiry and the removal of unnamed bytes run once right
  after start, then every `STORE_SWEEP_PERIOD`. A process that restarts often still
  cleans up.

## 6. Errors

| Status | Means | Consumer does |
|---|---|---|
| 400 | `space` or `name` breaks its alphabet, or the body cannot be read | fix the name; retrying will not help |
| 404 | no such file | treat it as absent |
| 413 | body over 512 MiB | do not retry |
| 503 | MinIO or Postgres unavailable | retry later |

## 7. Check

On a running instance with its own Postgres and MinIO:

```bash
S=${STORE_URL:-http://localhost:8094}
curl -fsS $S/health
printf hello > /tmp/a && printf world > /tmp/b
curl -fsS -X PUT --data-binary @/tmp/a -H 'Content-Type: text/plain' $S/v1/test/K-1.a
curl -fsS $S/v1/test/K-1.a                                   # hello
curl -fsS -X PUT --data-binary @/tmp/b $S/v1/test/K-1.a      # replace
curl -fsS $S/v1/test/K-1.a                                   # world
curl -fsS -X PUT --data-binary @/tmp/b $S/v1/test/K-2.b
curl -fsS "$S/v1/test?prefix=K-1." | grep -o '"name"' | wc -l  # 1
curl -s -o /dev/null -w '%{http_code}\n' $S/v1/test/no.such  # 404
curl -s -o /dev/null -w '%{http_code}\n' -X PUT --data-binary x "$S/v1/Test/x"  # 400
curl -fsS -X DELETE $S/v1/test/K-1.a && curl -fsS -X DELETE $S/v1/test/K-2.b
```

Plus a test against a real MinIO: equal bytes under two names are one object, and
the object goes once neither name points to it.
