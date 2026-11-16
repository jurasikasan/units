# apps

Applications assembled from units. An app follows whatever outside standard
its job needs — a UI protocol, a model provider's API, an agent loop of its
own. A unit stays a broader concept than any one app or standard.

## Rules

- An app consumes units through what their `unit.yaml` provides: HTTP, MCP,
  events. No unit imports or names an app.
- A unit changes for an app only when the change makes sense for the unit on
  its own. `subjects` learning to look up by nick is fine; `subjects` learning
  what an agent turn is is not.
- An app has its own directory, `check.sh` and a showcase that brings up the
  units it needs.

## The first app: an agent over units

Decided 2026-11-16. It takes over what bot's decision_core, api and ui do.
bot stays as it is, untouched, until this app covers what bot is used for; it
is read for why things were done there.

**Shape**

- The agent loop is ours: one synchronous turn, in process. No loop over
  Redis streams.
- Toward people: [AG-UI](https://docs.ag-ui.com) over HTTP + SSE; the thread
  id is a field of the run input, never text. The UI is assistant-ui's AG-UI
  runtime (`@assistant-ui/react-ag-ui`), not a chat UI or a WebSocket protocol
  of our own.
- Toward models: LiteLLM, OpenAI-compatible. The gateway picks providers.
- Tools: MCP to the units' own servers, spec 2026-07-28. No hub of our own:
  direct calls while the servers are few, or LiteLLM's MCP gateway if it
  speaks that spec (not checked).
- Storage is the units': `threads` holds what was said, `subjects` who
  exists, `task_board` the work, `artifact_store` the files, `job_runner` the
  runs.
- Voice from the first slices, through the same core: a chat continues as a
  call in the same thread. Audio goes over `web_audio_call`, `senses/hearing`
  and `abilities/speaking`; the turn goes through the core and AG-UI events.
  So the core's contract has, from the start: a running turn can be
  interrupted from outside and the model decides whether to stop; events
  reach a running turn; the thread keeps what was played, not what was
  generated.

**Order**

1. `LESSONS.md` from bot — its invariants with links to the commits that paid
   for them — and the core's contract.
2. Slice 1, text chat: assistant-ui → AG-UI → core → LiteLLM → units' MCP
   tools, history in `threads`.
3. Slice 2: the same thread continued as a call.
4. Dispatcher on `board_events`: an entry is a wake-up, not the truth; claim
   with `PUT status {from}`.
5. Stages as the loop's own data (what bot's stage_executor does by posing as
   a model).

**Standards to take**

- MCP 2026-07-28: `task_board`'s MCP made stateless; `job_runner`'s `run` /
  `peek` as the Tasks extension; MRTR (`input_required`) for a tool that needs
  a person's answer. Roots, Sampling, Logging and HTTP+SSE are deprecated —
  not in new code.
- AG-UI 1.0 toward people; A2UI later, for UI the agent draws; Pipecat's RTVI
  to look at for the browser side of a call.
- OTel GenAI semantic conventions, version pinned: one trace from a task taken
  through the turn, model calls, MCP and `job_runner`, with time and cost.
- CloudEvents on Redis streams stays the units' event bus
  ([contracts/events.md](../contracts/events.md)).
- `AGENTS.md` next to `CLAUDE.md`, so agents other than Claude read it;
  `SKILL.md` as the format for skills.
- A2A: known, not built, until our agents talk to someone else's.

**Open**

- Storage backend for `artifact_store` and `job_runner`: MinIO Community ships
  no images any more. SeaweedFS or Garage, checked in a showcase.
- `auth_service` or Authelia / Pocket ID. Taking one retires a unit.
- `engines/tts/piper` runs `piper-tts` 1.8, GPL-3.0 since 1.3. An older pin
  gains little, since espeak-ng inside is GPL-3 too; replace the engine or
  leave it.
- A budget per agent with a hard stop, and where it lives.

**Already there, no task needed**

- `task_board` answers "ready": `ready=true` on the task list — approved, no
  open dependency, no open subtask.
- `tools/evals`: past commits redone by an agent in `job_runner`, the
  reference commit's tests as the hidden judge, false green as the metric.
