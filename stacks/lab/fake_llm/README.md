# fake_llm

A model that answers from files: [WireMock](https://wiremock.org) with the
stub mappings in `mappings/`, OpenAI-shaped. LiteLLM reaches it as the
`fake` model. The mappings came from bot's fake_llm; `#first_message` is the
lab's own, and the default answer is quicker (3 s).

A case is chosen by a `#tag` in the last message, when that message is the
user's:

| tag | answer |
|---|---|
| none | a default text, a few words at a time |
| `#first_message` | the first user message of the conversation, word for word: the conversation got through whole |
| `#plain_text_response` | `plain_text_response pass` |
| `#fail_429`, `#fail_500` | that status with an OpenAI error body |
| `#hang` | no byte for 2 s, then a normal answer |
| `#late` | no byte for 20 s, then `#hang`'s answer: a reply stopped before it said anything (stream only) |
| `#slow` | `Counting slowly: one, two, … ten.`, a few words at a time over 30 s: a reply to stop while it is written (stream only) |
| `#tool_sum` | a call of `everything__get-sum` with `{"a": 2, "b": 3}`, in fragments, its id holding odd characters as Gemini's do (stream only) |
| `#draw` | a call of `images__draw` (tools/images) with `{"prompt": "a red circle on white"}`, in fragments (stream only) |
| `#think_glm` | reasoning in GLM's shape (`reasoning_content` and the same as `reasoning`), then `Thought it over.` (stream only) |
| `#think_gemini` | reasoning in Gemini's shape, then a call of `everything__get-sum` with thought signatures (`FAKE_SIG_1`) on the message, on the call and in its id; after the tool, when the signatures came back with the call, `The tool said: …; its signature came back.` and a new signature (`FAKE_SIG_2`), no reasoning shown (stream only) |
| `#signatures_back` | `Both signatures came back.` when both are on answers in the history; otherwise the default (stream only) |
| `#which_model` | `Asked as ` and the model the request names (`fake-1`, `fake-text-1`): which group a run went to (stream only) |
| `#attached` | `I see a picture, and the file says ZEBRA-7.` when the message carries a PNG as an `image_url` part and `note.txt` holding `ZEBRA-7` as a text part; otherwise the default (stream only) |

When the last message holds the mind's placeholder for a part the model
cannot take (`[an image is attached here, not shown to you: …]`), the
answer is `I am told of a picture, not shown it.` (stream only).

When the last message is a tool's result, the answer is `The tool said: ` and that result (stream only).

OpenAI's audio API, for `fake-stt` and `fake-tts`: any sound sent to
`/v1/audio/transcriptions` is heard as `What the fake ear heard.`, and any
words sent to `/v1/audio/speech` are a quarter-second beep, a WAV.

OpenAI's Images API, for `fake-images`: any prompt sent to
`/v1/images/generations` is the same picture, a red circle on white, a
128×128 PNG as `b64_json` (`images.json`).

Each tag has a `-stream` mapping (`"stream": true`) and a `-nostream` one.
