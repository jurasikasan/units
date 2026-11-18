# fake_realtime

A realtime model that answers from a script: OpenAI's Realtime schema over a
WebSocket at `/v1/realtime`, for the lab's call without a key. LiteLLM
reaches it as `fake-realtime` and `fake-realtime-full` (`openai/` with this
as `api_base`; LiteLLM hands OpenAI's events on as they are). Python and
`websockets`, one file, [`fake_realtime.py`](fake_realtime.py).

It hears by loudness, not words: in the PCM16 the client appends, 200 ms
of sound then 600 ms of quiet is an utterance (`FAKE_LOUD`, the RMS that is
sound, 500 on the int16 scale). Each utterance is answered by the next turn
of the script, round and round: what was heard and said on 2026-11-18 by
Gemini 3.1 Flash Live through LiteLLM (a question, the weather, a long
story, "stop"). The answer's voice is a 660 Hz tone, 200 ms a word, sent
at the pace it would be spoken; sound while it speaks interrupts it.

The flow is the model's name, which LiteLLM hands on as `?model=`:

| model | flow |
|---|---|
| `fake-realtime` | what Gemini Live sent through LiteLLM: the user's transcript once, after the utterance; no `speech_started`/`stopped`/`committed`, no transcript `.done`; an interruption as a `response.done` `completed` with no usage before the answer's `output_audio.done`, then a second `response.done` for a response never created; `truncate` taken and dropped |
| `fake-realtime-full` | the schema's whole set: `speech_started`, `speech_stopped`, `committed`, transcript `.done`, an interrupted answer done as `cancelled`, `truncate` answered with `conversation.item.truncated` |

`session.update` is answered with `session.updated`; anything else it is
sent is dropped. The recorded flow and what it lacked:
[contracts/edges.md](../../../contracts/edges.md), Probed.
