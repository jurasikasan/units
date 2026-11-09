# devices/communications/sip_phone

What it promises is `CONTRACT.md`; this file is for working inside it.

- `internal/call` — the SIP side: dial, inbound INVITEs and answer, the dialog
  until it ends, listen and hangup, and the call events. sipgo's dialog client
  and server; the end reasons are mapped in one place. On answer it opens the call's audio_in and forwards
  what arrives — 48 kHz L16 downsampled to 8 kHz — onto the call's RTP leg,
  filling the leg with silence while audio_in is quiet (CONTRACT §2.2), and mirrors the far end's audio out to the `listen` destination.
- `internal/audioin` — the per-call UDP receiver on a port from
  `AUDIO_IN_PORT_MIN`..`AUDIO_IN_PORT_MAX`: one listener, big-endian L16, and
  a socket buffer wide enough for a full 1932-byte frame (a 12-byte RTP header
  plus 960 int16 samples).
- `internal/device` — the calls together, as the contract's tools see them; a
  call's audio_in closes before its `ended` goes out.
- `internal/rtp` — the per-call
  `Session` owns the socket, symmetric RTP and the inbound loop; `Out` is the
  `listen` egress — one destination, its own sequence/timestamp, its SSRC.
- `internal/farend` — the far end of the tests: an in-process sipgo UA whose
  script is picked by the number dialled, and `Caller`, which calls phone as a
  trunk does. check.sh needs no docker, so sipp is for
  whole-stack runs only.

## Inbound calls (contract revision 4, §2.6, §6.1)

An INVITE gets `100`, `180` and `phone.call.incoming`, then rings until
`answer` (`200` with one G.711, the first of PCMA/PCMU in the offer's order),
`hangup` (`603`), the caller's CANCEL (`487`) or `INBOUND_ANSWER_TIMEOUT`
(`480`). Before ringing: `403` from a source not on `SIP_INBOUND_ALLOW`, `503`
at `PHONE_MAX_CALLS` (outbound and inbound together), `488` for an offer with
no G.711 — none of them with events. From `answered` on it is the same call as
an outbound one; no inbound RTP for `RTP_IDLE_TIMEOUT` ends it `failed`.

The whole call runs inside sipgo's INVITE handler, on purpose: sipgo terminates
an INVITE transaction whose handler returns before a final response, which
would kill every ringing call at once.

## listen (contract revision 3, §2.3)

`listen(call_id, to)` sends the far end's audio of an **answered** call to
`to` (`host:port`, UDP) as RTP: the G.711 the call carries (PCMU 0 or PCMA 8),
**as it arrives from the trunk, no transcoding**, in 20 ms packets with
phone's own sequence (+1) and timestamp (+160 samples per packet), and one
SSRC per call. One destination per call: a second `listen` moves the stream
to the new address and the old one gets nothing more; `to: ""` stops it.
Sending stops with the call. Refusals: `unknown_call`, `not_answered`
(before answered), `bad_target` (`to` not a resolvable `host:port`). On
success the result is `{"ok": true}` — including for `to: ""`.

`to` is an address on the box network, like audio_in: it is never published
by the box and needs no environment variable of its own.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `SIP_TRUNK` | empty | `host:port` every call goes to; empty = the line only takes calls, `dial` ends `failed` |
| `SIP_LISTEN` | `0.0.0.0:5070` | where SIP is received; its port goes into Contact |
| `SIP_ADVERTISE_ADDR` | the local address toward the trunk | IP in Contact and SDP; set it by hand behind NAT |
| `SIP_USERNAME`, `SIP_PASSWORD` | empty | answer a 401/407 digest challenge |
| `SIP_DEFAULT_FROM` | empty | caller ID when `dial` gives none; empty leaves it to the trunk |
| `SIP_FROM_ALLOW` | empty | comma-separated caller IDs `dial` accepts; empty accepts any |
| `SIP_STRIP_PLUS`, `SIP_NUMBER_PREFIX` | `false`, empty | E.164 → what the trunk dials |
| `RTP_PORT_MIN`, `RTP_PORT_MAX` | `42000`, `42199` | one UDP port per call |
| `AUDIO_IN_HOST` | `phone` | host part of the audio_in address the answered event reports |
| `AUDIO_IN_PORT_MIN`, `AUDIO_IN_PORT_MAX` | `43000`, `43199` | one UDP port per call, freed when the call ends |
| `PHONE_MAX_CALLS` | `4` | calls at once, both ways; `dial` past it is `capacity`, an INVITE `503` |
| `INBOUND_ANSWER_TIMEOUT` | `30s` | a ringing inbound call with no `answer`/`hangup` gets `480` |
| `RTP_IDLE_TIMEOUT` | `60s` | an answered inbound call with no inbound RTP this long ends `failed` |
| `SIP_INBOUND_ALLOW` | empty | comma-separated source IPs allowed to INVITE; empty accepts any |
| `RING_TIMEOUT_DEFAULT_S`, `RING_TIMEOUT_MAX_S` | `30`, `60` | `ring_timeout_s` when left out, and its ceiling |
| `REDIS_ADDR` | `localhost:6379` | where `phone_events` goes |
| `PORT` | `8080` | MCP (streamable HTTP) and `GET /health` |

## Running it

```bash
bash check.sh
docker compose -f docker-compose.sms-voice.yml up -d --build
```

It runs in the SMS → voice app (`docker-compose.sms-voice.yml`), dials `sipp`
(a callee that answers everything) unless `SIP_TRUNK` points elsewhere, and
publishes only MCP on `127.0.0.1:8098`. That app's own mcp-hub lists its tools
as `phone.dial`, `phone.answer`, `phone.listen` and `phone.hangup`; it starts after phone is
healthy, because the hub reads tool lists once at startup. A `traceparent` in
the call's
`_meta` goes through the hub and onto every event of the call.

## Showcase

```bash
cd showcase && docker compose up --build --abort-on-container-exit demo
```

Two lines, `alice` and `bob`, alice's trunk being bob. alice dials, bob answers
when `incoming` arrives, a 440 Hz tone goes into alice's `audio_in`, bob's
`listen` sends the call's audio to the demo, which counts the packets; alice
hangs up, and both lines' events are printed.

## Check

```bash
bash check.sh
```
