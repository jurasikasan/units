# drivers/model

A chat model holding subjects' reins: woken by threads' events, it answers
in the thread as the subject, through `minds/assistant`. What it promises
is `CONTRACT.md`; this file is for working inside it.

- `internal/driver/driver.go` — holds (`HOLDS`, found in `subjects`, again
  until found), the consumer group on `threads_events`, one lane per subject
  and thread, the answer, the events on `driver_events`.
- `internal/driver/prompt.go` — the prompt from the thread's tail: roles
  computed, who said it in the words, pictures and PDFs as AG-UI parts.
- `internal/driver/mind.go` — the AG-UI run, through the protocol's Go SDK
  (its SSE client and event decoder).
- `internal/driver/world.go` — the clients of `threads` and `subjects`.
- `internal/driver/driver_test.go` — the prompt, a run against a fake mind,
  a failure said only on the bus, who wakes it; the consumer group against a
  real Redis (`DRIVER_TEST_REDIS_ADDR`; `check.sh` starts one in docker).
- `showcase/` — the driver holding `alice-assistant` with a mock model
  (LiteLLM answering itself), threads, subjects, the bus and the mind; the
  demo says hello as alice straight into threads and reads the answer back:
  `docker compose -f showcase/compose.yaml up --build -d --wait driver && docker compose -f showcase/compose.yaml run --rm demo`.

Config: `HOLDS`, `THREADS_URL`, `SUBJECTS_URL`, `MIND_URL`, `REDIS_ADDR`
(waited for at boot), `TAIL` (50), `GROUP` (`drivers-model`), `CATCHUP`
(2m), `PORT` (8097).

```bash
bash check.sh
```
