abilities/speaking: a deadline per phrase — a stalled synthesis fails the phrase instead of silencing the call.

A phrase has no deadline of its own: when the TTS engine stalls, the call goes silent for as long as the HTTP client lets it (30 s per piece).

Change:
- `speak` takes an optional `synthesis_timeout_ms` (> 0): how long the line may wait for a piece of this phrase's audio. `SPEECH_SYNTHESIS_TIMEOUT` (default `10s`) is the session's deadline when a `speak` names none.
- The clock starts when the piece is *due* — when the phrase's turn comes, or the piece before it has played — not when its synthesis began; time spent synthesising while something else played does not count. It bounds silence, not length: a long phrase whose pieces come in time plays to the end.
- Past the deadline the phrase ends there: `speak-complete` with cause `error`, `spoken` up to that piece, its synthesis cancelled, and the next phrase plays.
- `synthesis_timeout_ms` ≤ 0 is `bad_argument`.
- CONTRACT.md: §2.2 gets the argument and the paragraph above, the error table gets the bad_argument line, the `error` cause names the deadline, the config table gets `SPEECH_SYNTHESIS_TIMEOUT`; revision 5. README if it lists the config.

Go names the change must have (tests elsewhere will use them):
- `internal/mouth`: `type Request struct { Text, Voice string; SynthesisTimeout time.Duration }` (zero = wait as long as synthesis takes) and `func (m *Mouth) SpeakWith(r Request) (string, error)`; `Speak(text, voice)` stays and is `SpeakWith` with no deadline.
- `internal/speech`: `Config.SynthesisTimeout time.Duration`; `type SpeakOptions struct { SynthesisTimeout time.Duration }` (zero = the session's); `func (sp *Speech) SpeakWith(sessionID, text string, opt SpeakOptions) (string, error)`, a negative timeout an error starting with `bad_argument`; `Speak` stays as `SpeakWith` with no options.
- The MCP `speak` tool's arguments get `synthesis_timeout_ms` (optional integer); `cmd/app` reads `SPEECH_SYNTHESIS_TIMEOUT`.

Add tests that show a stalled engine failing one phrase while the next plays, and the bad argument.
