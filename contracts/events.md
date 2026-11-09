# Events on Redis

How a unit tells the world what happened: one entry per event on a Redis
stream, in CloudEvents 1.0. Every unit that publishes events follows this and
names its stream and types in its own `CONTRACT.md`.

## Envelope

CloudEvents 1.0 in **binary mode**: each attribute is a field of the stream entry
with a `ce_` prefix, the payload is the `data` field. CloudEvents has no Redis
binding; this is its Kafka binding with stream fields in place of headers. The
prefix also keeps the attributes apart from anything else in an entry.

| CloudEvents attribute | Stream field | Value |
|---|---|---|
| `specversion` | `ce_specversion` | `1.0` |
| `id` | `ce_id` | UUID, unique per source; consumers deduplicate on `ce_source` + `ce_id`, never on the Redis entry id |
| `source` | `ce_source` | the unit's path in this repository, e.g. `/senses/hearing` |
| `type` | `ce_type` | see below |
| `subject` | `ce_subject` | the id of the thing the event is about (a call, a session) |
| `time` | `ce_time` | RFC 3339, UTC, milliseconds |
| `datacontenttype` | `ce_datacontenttype` | `application/json` |
| `traceparent` (distributed tracing extension) | `ce_traceparent` | W3C trace context, copied from whatever caused the event |
| — | `data` | the payload, JSON |

Each publishing unit writes to its own stream, named in its `CONTRACT.md` and in
`unit.yaml` (`provides.events.stream`).

## `type`

`<resource>.<event>` or `<resource>.<entity>.<event>`, lower case.

The resource is a standard one where a standard exists, so that a consumer who
knows the standard knows the vocabulary: hearing publishes MRCPv2's recognizer
events (`speechrecog.start-of-input`, `speechrecog.recognition-complete`),
speaking its synthesizer's (`speechsynth.speak-complete`). Where there is none,
the resource is the unit's own name and the event is in the past tense
(`phone.call.answered`).

No version in the name: a breaking change to the payload is a new `type`.

## Trace

One cause, one trace. Whoever starts the work — an incoming call, a request —
starts the trace; every hop carries it on: the W3C `traceparent` header on HTTP,
`ce_traceparent` on events, the MCP call's metadata into a unit. Whoever acts on
something with a `traceparent` passes it on unchanged, or as a child span.

## One entry, whole

```
XADD hearing_events * \
  ce_specversion     1.0 \
  ce_id              7f1c2e9a-3b4d-4c51-9a0e-5d2f8b6c1a07 \
  ce_source          /senses/hearing \
  ce_type            speechrecog.recognition-complete \
  ce_subject         hs-0b8e41f2 \
  ce_time            2026-10-29T10:15:03.120Z \
  ce_datacontenttype application/json \
  ce_traceparent     00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01 \
  data               '{"request_id":"utt-5c21d0e7","completion_cause":"success","text":"Hello?","language":"en","started_at":"2026-10-29T10:15:02.310Z","ended_at":"2026-10-29T10:15:02.980Z"}'
```
