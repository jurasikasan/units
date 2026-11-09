# sip_phone — contract (C1)

Revision 5. Revision 2 took the mouth out of the line (speaking is a unit of its own,
`abilities/speaking`); 3 added the call's audio out, for a listener such as
[`senses/hearing`](../../../senses/hearing/CONTRACT.md) (`listen`, §2.3); 4 added inbound
calls (`incoming`, `answer`, §2.6, §6.1); 5 has the line hold silence while `audio_in` is
quiet (§2.2).

## 1. What it is

A device: a telephone line. It places calls, carries audio into them and reports what
happened.
It executes and reports; it never decides. Whoever drives it — a script, a
dialogue engine, an LLM — calls its tools and reads its events.

- **Tools** over MCP, through mcp-hub under the server name `phone`: `dial`, `answer`,
  `listen`, `hangup`. Each returns at once; what follows comes as events.
- **Audio into a call** is RTP sent to the call's `audio_in` address, which comes with
  `answered` (§2.2). Who sends it — the speech apparatus, a recorder, another line — is
  not phone's business.
- **Audio out of a call** — what the far end says — is RTP phone sends to the address
  given to `listen` (§2.3).
- **Events** on the Redis stream `phone_events`, in the envelope of
  [`contracts/events.md`](../../../contracts/events.md).
  `ce_source` is `/devices/communications/sip_phone`, `ce_subject` is the `call_id`.

Calls go both ways. An outbound call starts with `dial`. An inbound call starts as an
event nobody asked for, `phone.call.incoming`; phone rings it and waits for a driver to
`answer` or `hangup`. From `answered` on, both are the same call: `audio_in`, `listen`,
`hangup`, `ended`.

### What it does not do

- does not create chats or situations and does not know which one a call belongs to —
  a consumer keeps `call_id` → its own context;
- does not speak: no TTS, no voices, no texts. It carries into the call whatever
  arrives at `audio_in`, narrowed to what the call carries (G.711 at 8 kHz);
- does not greet, fill pauses with words or sounds, or retry a call. Silence on the
  wire is another matter: keeping the RTP going is the line's (§2.2);
- does not decide whether to take a call, when to pick up, or who answers: it reports
  `incoming` and rings until told. How long to let it ring before answering (a random
  hold, say) is the driver's;
- does not hear: it carries the far end's audio to whoever `listen` names. Echo
  suppression and what counts as speech are the hearing apparatus's; whether anyone
  listens at all is the driver's choice, not a flag here.

## 2. Tools

Errors are MCP tool errors (`isError: true`) whose text starts with a code from §2.5.
The examples show `arguments` and `structuredContent`.

### 2.1 `dial(to, from?, ring_timeout_s?)` → `call_id`

Starts a call and returns before anyone answers.

| Argument | Required | Meaning |
|---|---|---|
| `to` | yes | E.164: `+` and digits (§4) |
| `from` | no | caller ID as the caller wants it, passed on as given (§4); empty = the trunk's own |
| `ring_timeout_s` | no | how long to let it ring; default and ceiling from env |

```json
{"to": "+380441234567", "from": "+380501112233", "ring_timeout_s": 30}
```
```json
{"call_id": "call-0b8e41f2"}
```

Over the call limit `dial` fails at once with `capacity`; it does not queue (§5).

### 2.2 Audio into the call

Once a call is answered, phone takes RTP for it at `audio_in` — a `host:port` it names
in `phone.call.answered` (§3) — and forwards it into the call.

- **What it accepts:** L16, 48 kHz, mono, payload type 96, 20 ms packets — what
  [`speech`](../speech/CONTRACT.md) §4 sends. phone resamples to 8 kHz and encodes the
  call's G.711. Opus (payload type 111) when speech sends it.
- **One sender:** the first SSRC that arrives owns the call's `audio_in`; packets with
  another SSRC are dropped.
- **Pace is the sender's:** phone forwards packets as they come, with no queue beyond a
  small jitter buffer.
- **phone holds the line:** while nothing arrives at `audio_in` for 60 ms (three
  frames), the call carries G.711 silence, one packet per 20 ms, on the same stream
  (sequence and timestamp continuing), until audio arrives again — from `answered` on.
  A trunk drops a call whose RTP stops for a few seconds, and a sender between two
  utterances, or with nothing to say yet, is not the one to know that.
- `audio_in` closes with the call. It is meant for the box's internal network, not for
  the world: its port range is not published (§6).

### 2.3 `listen(call_id, to)`

Starts sending the far end's audio of an answered call as RTP to `to` (`host:port`,
UDP) — e.g. a hearing session's `audio_in`.

- **What it sends:** the G.711 the call carries (PCMU payload type 0 or PCMA 8), as it
  arrives from the trunk, in 20 ms packets with phone's own sequence, timestamp and one
  SSRC per call. No transcoding, no widening of the band.
- **One destination per call:** a second `listen` moves the stream to the new `to`;
  `to: ""` stops it.
- Sending stops with the call.

```json
{"call_id": "call-0b8e41f2", "to": "hearing:44017"}
```
```json
{"ok": true}
```

Errors: `unknown_call`, `not_answered` (before `answered`), `bad_target` (`to` is not
`host:port` or does not resolve).

### 2.4 `hangup(call_id)`

Ends the call: BYE if answered, CANCEL if still ringing; `audio_in` closes. The `ended`
event follows (§3).

```json
{"call_id": "call-0b8e41f2"}
```
```json
{"ok": true}
```

Hanging up a call that already ended is not an error: `{"ok": true}`, no second `ended`.

On a ringing inbound call `hangup` declines it with `603 Decline`; `ended{declined}`
follows.

### 2.5 Error codes

| Code | When |
|---|---|
| `capacity` | the call limit is reached (§5) |
| `invalid_number` | `to` is not E.164 |
| `from_not_allowed` | an allowlist is configured and `from` is not on it (§4) |
| `unknown_call` | no such call (`hangup`, `listen` of an id phone never gave) |
| `not_answered` | `listen` before the call is answered |
| `bad_target` | `listen` with a `to` that is not `host:port` or does not resolve |
| `not_ringing` | `answer` of a call that is outbound, already answered or ended |

### 2.6 `answer(call_id)`

Picks up a ringing inbound call: `200 OK` with the SDP answer (§6.1). `answered` follows
with `audio_in`, as for an outbound call.

```json
{"call_id": "call-in-6c0e93d1"}
```
```json
{"ok": true}
```

Errors: `unknown_call`; `not_ringing`.

## 3. Events

Stream `phone_events`. `data` is JSON; every event carries `call_id`.

| `ce_type` | When | `data` beyond `call_id` |
|---|---|---|
| `phone.call.incoming` | an INVITE arrived and is ringing (`100`, `180` sent) | `from`, `to`, `offered_at` |
| `phone.call.ringing` | the far end reports ringing (180/183). Not guaranteed — some trunks send nothing until the answer | — |
| `phone.call.answered` | 200 OK, media up | `answered_at`, `audio_in` |
| `phone.call.ended` | the call is over, for any reason | `reason`, `sip_status`, `duration_ms` |

Guarantees a consumer may build on:

- every `dial` that returned a `call_id` ends with exactly one `phone.call.ended`, and it
  is the call's last event;
- `ringing` before `answered` before `ended`, when they occur;
- every `incoming` ends with exactly one `phone.call.ended`, its last event; `incoming`
  before `answered` before `ended`. An inbound call never has `ringing` (that event is
  the far end ringing for us).

In `incoming`:

- `from` — the caller's number as the INVITE's From user part carried it; may be
  `anonymous` or not a number at all. phone passes it on, does not judge it.
- `to` — the number called (the Request-URI user part): which of our numbers rang.

```json
{"call_id": "call-in-6c0e93d1", "from": "+380501112233", "to": "380441234567", "offered_at": "2026-11-01T15:02:11.410Z"}
```

```json
{"call_id": "call-0b8e41f2", "answered_at": "2026-10-29T10:15:03.104Z", "audio_in": "phone:43012"}
```

What the callee heard is not phone's to report in this revision: speech reports what it
sent (`heard` in `speechsynth.speak-complete`). If a driver ever needs the wire's truth, it is a phone
event to add then.

### `ended` reasons

A closed list:

| `reason` | Means | `sip_status` |
|---|---|---|
| `remote-hangup` | answered, then the callee hung up | — |
| `local-hangup` | answered, then we did (`hangup`) | — |
| `cancelled` | we hung up while it was still ringing | — |
| `busy` | 486 / 600 | the code |
| `no-answer` | rang until `ring_timeout_s`; we sent CANCEL | — |
| `declined` | outbound: the callee sent 603; inbound: we declined with `hangup` | 603 |
| `abandoned` | inbound: the caller gave up (CANCEL) before we answered | — |
| `unattended` | inbound: nobody answered or declined within `INBOUND_ANSWER_TIMEOUT`; phone sent `480` | 480 |
| `failed` | anything else: no route, 404/484, the trunk refused the caller ID, a network error | the code, or 0 if no response came |

```json
{"call_id": "call-0b8e41f2", "reason": "remote-hangup", "sip_status": 0, "duration_ms": 14820}
```

What a reason means for an errand (delivered, interrupted, unanswered) is decided by the
consumer, not here.

## 4. Numbers and caller ID

- `to` is E.164. Turning it into what the trunk wants (no `+`, a prefix) is phone's job,
  from env.
- `from` is passed on as given — it may come from an incoming SMS, so it can be anything
  the sender used. phone does not judge it; if the trunk refuses it, the call ends
  `failed` with the trunk's code. An optional allowlist in env limits it; with no
  allowlist, phone tries whatever it is given. Access to `dial` is the control.

## 5. Limits

- At most `N` calls at once (env). Over it, `dial` fails with `capacity` straight away;
  whether to wait and try again is the caller's call.
- `ring_timeout_s`: a default and a ceiling, both from env.
- One sender per call's `audio_in` (§2.2).
- Inbound counts against the same limit as outbound. An INVITE over it gets `503` at
  once and no events: a call never offered to a driver is not a call.

## 6. On the wire

**SIP 5070/udp, RTP 42000–42199/udp**. G.711 only; PCMA or PCMU in the
far end's order of preference.

`audio_in`: **43000–43199/udp**, one port per call, on the internal network only — not
published. `AUDIO_IN_HOST` is the name senders reach phone by (default `phone`).

### 6.1 Inbound SIP on the wire

On the same SIP port and RTP range as outbound, as a carrier SBC expects.

- **INVITE** → `100 Trying`, `180 Ringing`, `incoming`. No early media: nothing is sent
  before `200 OK`.
- **Codec:** one G.711 flavour in the answer, the first of PCMA/PCMU in the offer's own
  order of preference. An offer with neither gets `488`, no events.
- **re-INVITE** keeps the socket and codec and may move the far end's RTP address.
  **OPTIONS** → `200` with `Allow`; **UPDATE** → `200`. **CANCEL** before the answer →
  `487` to the INVITE, `ended{abandoned}`; **BYE** → `ended{remote-hangup}`.
- One To-tag per call on both `180` and `200`; Contact on every dialog-establishing
  response; SDP lines end in CRLF.
- **RTP:** symmetric latching (RFC 4961) — audio goes where packets come from. Non-audio
  payload types (RFC 2833 DTMF, comfort noise) are dropped, not passed to `listen`. No
  inbound RTP for `RTP_IDLE_TIMEOUT` on an answered call → BYE, `ended{failed}`.
- Who may call in: `SIP_INBOUND_ALLOW` — a comma list of source IPs (the trunk's SBCs);
  empty = anyone. INVITEs from elsewhere get `403` and no events.

| Variable | Meaning | Default |
|---|---|---|
| `INBOUND_ANSWER_TIMEOUT` | ringing with no `answer`/`hangup` → `480`, `ended{unattended}` | `30s` |
| `RTP_IDLE_TIMEOUT` | no inbound RTP this long on an answered call → `ended{failed}` | `60s` |
| `SIP_INBOUND_ALLOW` | source IPs allowed to INVITE; empty = any | empty |
