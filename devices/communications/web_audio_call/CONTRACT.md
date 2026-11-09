# web_audio_call — contract (C6)

Its wire names (stream, event types, MCP server name) are still `webrtc_line`, the
name it was born under; changing them is a contract change of its own.

## 1. What it is

A device: a line to a browser. A browser calls in over WebRTC; the line carries audio
into the call and out of it, and reports what happened. It executes and reports; it
never decides. The twin of [`sip_phone`](../sip_phone/CONTRACT.md) — same buttons and audio
rules, another medium — and the first line where the call comes *in*.

- **Signalling** over HTTP: the browser posts its SDP offer, gets the answer (§2).
  Reached from outside only through nginx with `auth_request` to [`auth_service`](../../../auth_service/), like
  every other door; the line gets the caller as `X-Principal-Id`.
- **Tools** over MCP, through mcp-hub under the server name `webrtc_line`: `listen`,
  `hangup`.
- **Audio into a call** is RTP sent to the call's `audio_in` (from `connected`, §4).
- **Audio out of a call** — what the browser's microphone hears — is RTP the line sends
  to the address given to `listen`.
- **Events** on the Redis stream `webrtc_line_events`, envelope of
  [`contracts/events.md`](../../../contracts/events.md),
  `ce_source` `/devices/communications/web_audio_call`, `ce_subject` the `call_id`.

Receivers name an address, senders are told where to send — the rule of
speaking, [`senses/hearing`](../../../senses/hearing/CONTRACT.md) and sip_phone.

### What it does not do

- does not speak, listen, detect voice, transcribe, greet, fill pauses or record —
  those are organs and drivers;
- does not decide whether to take a call: the browser is already in when the line
  reports it. A driver that does not want the call hangs up;
- does not know chats, subjects or situations: it reports the caller's principal and
  nothing more;
- does not transcode for organs beyond Opus ↔ L16: Opus (and its cgo library) stays
  inside the line, so organs never need it.

## 2. Signalling

`POST /offer` with `{"sdp": "<offer>", "type": "offer"}` → `200`
`{"sdp": "<answer>", "type": "answer", "call_id": "wcall-…"}`.

- Non-trickle ICE: the answer carries all of the line's candidates; the browser does
  the same with its offer.
- One audio track each way, Opus 48 kHz. An offer without audio → `400`.
- Over the call limit → `503` `{"error": "capacity"}`, nothing is opened.
- No `X-Principal-Id` → `401`: the line is only ever behind nginx's auth.
- Candidates: the host's addresses, plus `WEBRTC_PUBLIC_IP` when set (behind NAT);
  media on UDP `WEBRTC_PORT_MIN`..`MAX`.

## 3. Tools

Errors are MCP tool errors whose text starts with the code.

### 3.1 `listen(call_id, to)`

Starts sending what the browser's microphone hears as RTP to `to` (`host:port`, UDP) —
e.g. a hearing session's `audio_in`. Decoded from Opus to **L16 48 kHz mono, network byte order (RFC 3551), payload
type 96**, 20 ms packets (960 samples), the line's own sequence, timestamp and one SSRC
per call. One destination per call: another `listen` moves the stream; `to: ""` stops
it. Sending stops with the call.

### 3.2 `hangup(call_id)`

Ends the call: closes the peer connection; `ended` follows. Hanging up an ended call is
`{"ok": true}`, no second event.

### 3.3 Error codes

| Code | When |
|---|---|
| `unknown_call` | no such call, or it has ended (`listen`) |
| `bad_target` | `to` is not `host:port` or does not resolve |

## 4. Audio into the call

RTP to `audio_in`, one UDP port per call from `WEBRTC_AUDIO_IN_PORT_MIN`..`MAX`
(internal network only), named `<WEBRTC_AUDIO_IN_HOST>:<port>`.

- **Accepts** L16 48 kHz mono, network byte order, payload type 96, 20 ms — what speech sends — and encodes
  it to Opus for the browser.
- **One sender:** the first SSRC with payload type 96 owns `audio_in`; others are
  dropped. Other payload types are dropped.
- **Pace is the sender's;** a small jitter buffer, no queue. When nothing arrives, the
  browser hears silence.
- `audio_in` closes with the call.

## 5. Events

| `ce_type` | When | `data` beyond `call_id` |
|---|---|---|
| `webrtc_line.call.connected` | ICE and DTLS are up, audio flows | `principal`, `audio_in`, `connected_at` |
| `webrtc_line.call.ended` | the call is over, for any reason | `reason`, `duration_ms` |

`ended` reasons, a closed list:

| `reason` | Means |
|---|---|
| `remote-hangup` | the browser closed the connection |
| `local-hangup` | `hangup` was called |
| `unattended` | nobody called `listen` or sent to `audio_in` within `WEBRTC_UNATTENDED_TIMEOUT` of `connected` |
| `failed` | ICE or DTLS failed, or the connection dropped without a close |

Guarantees: every `200` from `/offer` ends in exactly one `ended`, the call's last
event; `connected` comes before `ended` when it comes at all (an ICE failure gives
`ended{failed}` without `connected`).

`unattended` is the line protecting itself from a call nobody drives: a browser in a
silent call for ever. It decides nothing about the call's content.

## 6. Limits and config

| Variable | Meaning | Default |
|---|---|---|
| `WEBRTC_MAX_CALLS` | calls at once | `4` |
| `WEBRTC_UNATTENDED_TIMEOUT` | see §5 | `10s` |
| `WEBRTC_PUBLIC_IP` | an extra ICE candidate behind NAT | empty |
| `WEBRTC_PORT_MIN` / `MAX` | media UDP range, published to the world | `45000` / `45099` |
| `WEBRTC_AUDIO_IN_HOST` | the name senders reach the line by | `webrtc_line` |
| `WEBRTC_AUDIO_IN_PORT_MIN` / `MAX` | `audio_in` range, internal | `45100` / `45199` |
| `REDIS_ADDR` | where events go | — |
| `PORT` | signalling, MCP, `GET /health` | `8080` |

The media range is apart from sip_phone's (42000–42199, 43000–43199).

## 7. Open

- A test page: a static page that calls `/offer` with the microphone and plays the
  answer, for trying the line without the UI. Useful; belongs to whoever builds the
  probe, not to the contract.
- Video, data channels, more than one audio track.
- Trickle ICE, for faster setup on poor networks.
