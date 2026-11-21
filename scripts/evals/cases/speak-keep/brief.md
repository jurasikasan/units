abilities/speaking: kill_on_barge_in per speak — a phrase may ask to be finished.

Whether a phrase dies when talked over is today a trait of the whole call. `speak` gets `kill_on_barge_in` (MRCPv2's Kill-On-Barge-In, RFC 6787 §8.4.2), default `true`.

Behaviour:
- `false` keeps the phrase playing through `barge_in_occurred`; its `speak-complete` carries `kept_on_barge_in: true` (present only then). `stop` and `close` still cut it; then its cause is `cancelled`, otherwise `normal` when it plays out.
- `barge_in_occurred` decides per request, not by the active one as MRCP does: each request's own `kill_on_barge_in` holds. A cuttable phrase queued *behind* a kept one is cut at once — its synthesis abandoned, nothing of it played — but gets its `speak-complete` (`barge-in`, `spoken` empty) in its turn, after the kept one's. A kept phrase queued behind a cut one still plays.
- The `barge_in_occurred` call returns once the requests ahead of the first kept one have their `speak-complete`.
- CONTRACT.md: §2.2 the argument, §2.4 the per-request rule and ordering, the `speak-complete` event's new field `kept_on_barge_in`, §7 the difference from MRCP; revision 6.

Go names the change must have (tests elsewhere will use them):
- `internal/mouth`: `Request` gets `KeepOnBargeIn bool` (zero lets a barge-in cut it); `Played` gets `KeptOnBargeIn bool`; `func (m *Mouth) StopBargeIn() []string` — what a barge-in does: cuts the requests that let it, as above, and returns the ids of the requests whose `Played` are out by the time it returns. `Stop(cause)` stays and cuts everything.
- `internal/speech`: `SpeakOptions` gets `KeepOnBargeIn bool`; `barge_in_occurred` goes through `StopBargeIn`; the `speak-complete` event carries `kept_on_barge_in` when true.
- The MCP `speak` tool's arguments get `kill_on_barge_in` (optional boolean, default true).

Add tests: a kept phrase finishes through a barge-in; cuttable phrases around a kept one are cut in order; `stop` cuts a kept phrase.
