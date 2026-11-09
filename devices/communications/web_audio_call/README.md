# devices/communications/web_audio_call

A line to a browser (contract C6). A browser gets a real-time Opus uplink to
this service by offering WebRTC; the service then can (a) pass the browser's
audio to an organ that speaks raw L16 RTP on UDP, and (b) accept the organ's
audio and send it back to the browser.

Everything runs on one service port (default `8080`):

| Endpoint          | What it is                                                                 |
|-------------------|----------------------------------------------------------------------------|
| `POST /offer`     | WebRTC door: `{"sdp","type":"offer"}` → `{"sdp","type":"answer","call_id"}` |
| `GET /health`     | `200 {"status":"ok"}`                                                      |
| `/mcp`            | MCP (streamable HTTP) tools: `listen`, `hangup` — server name `webrtc_line` |

Events go to the Redis stream `webrtc_line_events` as CloudEvents 1.0
(envelope per [`contracts/events.md`](../../../contracts/events.md); the wire names keep
the line's first name, `webrtc_line`):

- `webrtc_line.call.connected` — ICE + DTLS up. `data`: `call_id`, `principal`,
  `audio_in` (`<WEBRTC_AUDIO_IN_HOST>:<port>`), `connected_at`.
- `webrtc_line.call.ended` — exactly one per accepted offer, always the call's
  last event. `data`: `call_id`, `reason`, `duration_ms`. Reasons:
  `remote-hangup`, `local-hangup`, `unattended`, `failed`.

## Calls

`POST /offer` needs `X-Principal-Id` (else `401`), must be a type-offer SDP
that contains an `m=audio` line (else `400`), and must not push the service
past `WEBRTC_MAX_CALLS` live calls (else `503 {"error":"capacity"}`, nothing
opened). The answer is non-trickle: the service waits for ICE gathering to
complete before responding, so all candidates are already in the SDP.

Media rides WebRTC on UDP ports from `WEBRTC_PORT_MIN`..`WEBRTC_PORT_MAX`
(default 45000–45099), one Opus 48 kHz mono track each way.

## Audio in (organ → browser)

Each call gets one UDP port from `WEBRTC_AUDIO_IN_PORT_MIN`..`MAX`
(default 45100–45199). The address organs use is
`<WEBRTC_AUDIO_IN_HOST>:<port>` (default host `webrtc_line`), announced in
the `connected` event. The wire is L16 (little-endian) 48 kHz mono inside
RTP: payload type **96**, 20 ms packets = 960 samples = 1920 bytes. The first
SSRC with PT 96 owns the socket; other SSRCs and other payload types are
dropped. Frames are Opus-encoded and sent to the browser. The socket closes
with the call.

## MCP tools

MCP over streamable HTTP at `GET/POST /mcp` (server name `webrtc_line`).

- `listen(call_id, to)` — start sending the browser's mic as RTP to `to`
  (L16 48 kHz mono, PT 96, 20 ms, sequence +1, timestamp +960, one SSRC per
  call). A further `listen` on the same call **moves** the stream; `to: ""`
  stops it. Errors: `unknown_call`, `bad_target` (tool error text starts with
  the code).
- `hangup(call_id)` — end the call; a second hangup on an ended call still
  returns ok and emits no second event.

## Unattended calls

If, within `WEBRTC_UNATTENDED_TIMEOUT` (default 10 s) after `connected`,
neither a `listen` nor accepted audio_in traffic arrives, the call ends with
`reason: unattended`.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | HTTP port (API, MCP, health) |
| `REDIS_ADDR` | `localhost:6379` | Redis for events (units/shared/redis) |
| `WEBRTC_MAX_CALLS` | `4` | live call cap |
| `WEBRTC_UNATTENDED_TIMEOUT` | `10s` | silence deadline before `unattended` |
| `WEBRTC_PUBLIC_IP` | *(empty)* | when set behind NAT, candidate IPs in the answer are rewritten to this IP |
| `WEBRTC_PORT_MIN` / `WEBRTC_PORT_MAX` | `45000` / `45099` | WebRTC media UDP range (published) |
| `WEBRTC_AUDIO_IN_HOST` | `webrtc_line` | host part of the audio_in address (published) |
| `WEBRTC_AUDIO_IN_PORT_MIN` / `WEBRTC_AUDIO_IN_PORT_MAX` | `45100` / `45199` | audio_in UDP pool (box-internal) |

## Build and run

```sh
cd webrtc_line
go build -o webrtc_line .
./webrtc_line
```

The Go build needs libopus (dev) — the included `Dockerfile` handles that
(`apk add opus-dev` in the builder, `opus` in the runtime image).

## Tests

`go test ./...` (via `check.sh`) runs a full stand-in suite: a pion peer
connection plays the browser, miniredis stands in for Redis. It verifies the
connected event, audio_in → browser decoding (440 Hz → 880 ± 44
zero crossings/s), `listen` stream format and move/stop, hangup reasons,
unattended shutdown, the 401/400/503 offer errors and the tool error codes.

## Showcase

```bash
cd showcase && docker compose up --build --abort-on-container-exit demo
```

`caller.py` stands in for a browser: aiortc calls the line with a 440 Hz tone,
the line's `listen` sends that audio to the script, a 220 Hz tone goes back
through `audio_in`, and the script counts both ways, hangs up and prints the
line's events.

## Check

```bash
bash check.sh
```
