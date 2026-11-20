# drivers/model

A chat model holding subjects' reins as one of its profiles, when reins
says so: woken by threads' events, it answers in the thread as the subject,
through `minds/assistant`; it greets first where its subject opened a
thread, and a stop cuts a reply. What it promises
is `CONTRACT.md`; this file is for working inside it.

- `internal/driver/driver.go` — profiles (`PROFILES`), whom they hold as
  reins says (`Sync`, `FollowReins` on `reins_events`), the consumer group
  on `threads_events`, one lane per subject and thread whose run can be
  cut (a stop: what was written said; the reins taken: nothing), the
  answer and the greeting, the events on `driver_events`.
- `internal/driver/prompt.go` — the prompt from the thread's tail: roles
  computed, who said it in the words, pictures and PDFs as AG-UI parts;
  the greeting's cue.
- `internal/driver/mind.go` — the AG-UI run, through the protocol's Go SDK
  (its SSE client and event decoder).
- `internal/driver/world.go` — the clients of `threads`, `subjects` and
  reins' inner `GET /holds`.
- `internal/driver/driver_test.go` — the prompt, a run against a fake mind,
  a failure said only on the bus, who wakes it, holds from reins, the
  greeting, a stop and a taking; the consumer group and `reins_events`
  against a real Redis (`DRIVER_TEST_REDIS_ADDR`; `check.sh` starts one in
  docker).
- `showcase/` — the driver with a `mock` profile (LiteLLM answering
  itself), reins handing it `alice-assistant` at alice's first visit,
  threads, subjects, the bus and the mind; the demo has alice come, reads
  the greeting in her first thread, takes her own avatar up and says hello
  through reins, reads the answer back; takes the assistant back, and the
  driver says nothing more as it:
  `docker compose -f showcase/compose.yaml up --build -d --wait driver && docker compose -f showcase/compose.yaml run --rm demo`.

Config: `PROFILES`, `REINS_URL`, `THREADS_URL`, `SUBJECTS_URL`, `MIND_URL`, `REDIS_ADDR`
(waited for at boot), `TAIL` (50), `GROUP` (`drivers-model`), `CATCHUP`
(2m), `PORT` (8097).

```bash
bash check.sh
```
