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
| `#tool_sum` | a call of `everything__get-sum` with `{"a": 2, "b": 3}`, in fragments, its id holding odd characters as Gemini's do (stream only) |

When the last message is a tool's result, the answer is `The tool said: ` and that result (stream only).

Each tag has a `-stream` mapping (`"stream": true`) and a `-nostream` one.
