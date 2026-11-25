# units

Self-sufficient services and libraries that are useful on their own and can be
assembled into applications: an LLM router, speech in and out, a phone line,
auth, a task board, a file store. Each unit lives in its own directory with its
own `go.mod`, and its own check script.

| unit | what it is |
|---|---|
| [`shared`](shared/) | small Go helpers the units have in common: HTTP, Redis, ops alerts, a fallback loop |
| [`notifier`](notifier/) | operational failures from a Redis stream to a Telegram chat, repeats folded |
| [`gate`](gate/) | the door in front of applications: Authelia's login page and nginx `auth_request`; programs hold short-lived tokens through `gate-token`; the application behind sees only `X-Principal-Id` |
| [`auth_service`](auth_service/) | login and sessions behind nginx `auth_request`; applications see only a principal id |
| [`subjects`](subjects/) | who exists: identities with a permanent nick, a type and free-form preferences, on their own Postgres |
| [`threads`](threads/) | what happened where: threads as append-only journals of entries — ActivityStreams kinds, typed content blocks, no roles — on their own Postgres; each append announced on its Redis stream |
| [`artifact_store`](artifact_store/) | named files in spaces: put, get, list, delete, read by sha256; identical bytes kept once, expiry per space; names in its own Postgres, bytes in MinIO or any S3 |
| [`task_board`](task_board/) | tasks and their flow: per-project handles, statuses with the moves allowed between them, assignees by nick, dependencies and subtasks, on their own Postgres; every move published as an event. An agent connects from [AGENTS.md](task_board/AGENTS.md) |
| [`job_runner`](job_runner/) | an isolated machine for agents: files in, a script runs under gVisor with no network unless asked, files out; answered when it ends, looked at with `peek` while it runs. An agent sets it up from [AGENTS.md](job_runner/AGENTS.md) |
| [`engines`](engines/) | speech engines behind OpenAI's audio API: piper, silero, voxcpm (TTS), whisper (STT); [`vad/silero`](engines/vad/silero/), speech detection per 32 ms window; and pictures behind OpenAI's Images API: [`images/diffusers`](engines/images/diffusers/) on our own GPU, offline, and [`images/cloudflare`](engines/images/cloudflare/) drawn at Cloudflare |
| [`senses/hearing`](senses/hearing/) | takes a sound stream in as RTP, finds the utterances, has them transcribed, reports them as MRCP recognizer events |
| [`abilities/speaking`](abilities/speaking/) | a mouth: text in through MCP, audio out as RTP through a TTS engine, MRCP synthesizer events saying what was spoken |
| [`tools/images`](tools/images/) | a tool that draws, the first of the tools agents call: one MCP tool, `draw`, a prompt in, a picture and a line of words out, from any OpenAI Images endpoint; the picture is the tool's |
| [`tools/web`](tools/web/) | a tool that looks at the web: two MCP tools, `search` (Brave's Search API; with no key, said not configured) and `fetch` (a public page's words, HTML as markdown), fetching from a process on an egress-only network that refuses every address that is not public |
| [`devices/communications/sip_phone`](devices/communications/sip_phone/) | a telephone line over SIP: places and takes calls, carries the call's audio both ways as RTP, reports what happened |
| [`devices/communications/web_audio_call`](devices/communications/web_audio_call/) | a line to a browser over WebRTC: the same buttons and audio rules as sip_phone, another medium |

More units arrive one at a time, each checked and given a showcase before it
lands here.

## Contracts and manifests

- [`contracts/events.md`](contracts/events.md): how a unit publishes events, as
  CloudEvents on a Redis stream.
- A unit that is a service carries a `unit.yaml`: what it provides (tools,
  events, HTTP calls), what it consumes, its config. `scripts/check_unit.py` holds
  each manifest to its unit's code and to the manifests of the units it
  consumes; each such unit's `check.sh` runs it.

## License

Apache-2.0, see [LICENSE](LICENSE).
