# faces/web_chat

A chat page: the conversation and a box to type in. It speaks
[AG-UI](https://docs.ag-ui.com) to whatever answers at `/api/run` on the same
origin, sending the whole conversation with each run and showing the events
that come back as they arrive. It has no server and decides nothing: static
files.

Built from [assistant-ui](https://www.assistant-ui.com) (MIT): its AG-UI
runtime (`@assistant-ui/react-ag-ui`) from npm, and its chat components from
its registry. React, Vite, Tailwind.

## Ours and upstream's

| | |
|---|---|
| `src/App.tsx`, `src/main.tsx`, `index.html` | ours: the runtime wired to `/api/run`, the theme |
| `src/upstream/` | **generated**: what assistant-ui's registry gives today (the thread, its parts, shadcn's base components, the stylesheet). Never edited by hand |
| `patches/` | our changes to upstream, each with why; applied on top of it |
| `upstream.sh` | `src/upstream` again from the registry, patches on top |

Upstream changes every week, and the way the standard chat client draws a
new kind of event (reasoning, tools, interrupts, generative UI) is news from
the UI's edge. So it stays as it comes: run `./upstream.sh` and
`git diff src/upstream package.json` shows what changed; a patch that no
longer applies says upstream changed where we had. A change of ours to
upstream's code is a new patch, never an edit in `src/upstream`; a component
of our own goes outside it, into a `Thread` slot (`components={{
AssistantMessage, Welcome, ComposerExtra }}`) where upstream offers one.

`src/upstream` is about 5,600 lines nobody here wrote: read it when the work
is about the UI's edge, not to learn the face.

## What it expects

| | |
|---|---|
| `POST /api/run` | an AG-UI agent: `RunAgentInput` in, events out as SSE; text messages are what it shows now |
| `GET /api/v1/models` | OpenAI's list of models, the one a run without a pick goes to first. The picker shows only when this answers |
| `POST /api/v1/audio/transcriptions` | OpenAI's: a recording in, `{text}` out. The mic shows only when this is answered |
| `POST /api/v1/audio/speech` | OpenAI's: `{input}` in, a sound out. Read-aloud shows only when this is answered |
| `GET /api/v1/realtime` | OpenAI's Realtime API, a WebSocket: a call. The call button shows only when this is answered |

A `401` from `/api/run` reloads the page: whatever door stands in front gets
to ask the person to log in. The face knows no door of its own.

## Which model

A picker in the composer's row offers the models `GET /api/v1/models`
lists (for the lab's mind, LiteLLM's groups: `fake`, `chat`, `lite`, …),
the first chosen (`src/Models.tsx`). It is assistant-ui's own
`model-selector`: the pick goes into the runtime's model context, which the
AG-UI runtime sends with each run as `forwardedProps.modelName`. The pick
holds while the page is open; a reload is the first one again. Nothing
answers there: no picker, and the agent's own default.

## Attachments

A picture, a text file or a PDF can go with a message (`src/attachments.ts`):
assistant-ui's own adapters for pictures (a `data:` URL) and text files (the
text, naming the file), and ours for a PDF (a file part). The AG-UI runtime
sends them as `image` and `document` parts, or text, with the message, and,
since the whole conversation goes with each run, with every run after. A
file over 10 MB is refused when attached: the attachment shows an error,
its reason when pointed at, and nothing is sent while it is there.

## Sound, turn by turn

The mic in the composer records until it is pressed again, then the
recording is transcribed whole and its text lands in the composer, to read
and send; a button on an answer reads it aloud (`src/voice.ts`). Both go to
OpenAI's audio API on the same origin, which picks the model and the
voice; the face sends no `model`. assistant-ui's own adapters for this are
the browser's Web Speech, which sends the sound wherever the browser's maker
chose, so they are not used. Each button shows only where the page is
answered (a `HEAD` that is not 404). Nothing is heard or played while it is
still coming: a transcription answers once, for the whole recording, and a
spoken answer plays when it has all come. A failure (no mic, the endpoint's
error) is said at the top of the page for a few seconds, in the endpoint's
own words. A call (sound both ways, live) is below.

## A call

A phone button in the composer starts a call: sound both ways, live, in
OpenAI's Realtime schema over a WebSocket to `/api/v1/realtime` on the same
origin (`src/realtime.ts`, assistant-ui's `RealtimeVoiceAdapter` with our
own session; its own adapters go to a provider with a key in the page). The
mic goes out as PCM16 at 24 kHz every 100 ms, echo cancelled by the
browser; the answer's sound plays as it comes. The page asks for the
session's formats, the model's own turn detection and a transcript of what
was said; it names no model and no voice. What each side said lands in the
thread as a voice conversation, the answer word by word. While the call
goes: mute (nothing is sent), hang up. An interruption is taken from
`input_audio_buffer.speech_started` while an answer plays, or from the
answer's `response.done` before its `output_audio.done` (all a model behind
LiteLLM was seen to send, `contracts/edges.md`): the sound stops and the
model is told how much was heard (`conversation.item.truncate`,
`audio_end_ms`). A close the page did not ask for, or the endpoint's
`error`, is said at the top of the page. Typed text does not go into a
call.

## Showcase

```bash
cd showcase
docker compose up -d --build
docker compose run --rm demo
```

Then http://localhost:8080. Behind the page is an agent of twenty lines that
says back what it was told (`showcase/echo_agent.py`): any AG-UI agent will
do, and `minds/assistant` is one.

## Developing

`npm run dev` serves the page with hot reload and sends `/api` to the
showcase's agent (`http://localhost:8089`), or to `API_URL`. `./check.sh`:
install, typecheck and build (upstream included: a type error there is news
too), lint (ours only), the manifest.

Light or dark follows the system's setting, and changes with it.

## Not yet

- One conversation, kept in the page: a reload forgets it. A list of threads
  comes when there is somewhere to keep them.
- No log-out button, no name of who is logged in: those are the door's, and
  the page does not know which door it stands behind.
- The bundle is 1.6 MB (460 KB gzipped), mostly what assistant-ui brings for
  markdown, attachments and tools.
