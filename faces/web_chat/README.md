# faces/web_chat

A chat page organised around who the person is on this device: the ghost,
who sees every thread of their avatars and does nothing, or one of their
avatars, seeing its threads and writing as it while its reins are held
here. Its chats are threads in the world, read and written through
[reins](../../reins/CONTRACT.md) on the same origin: what the person says
is appended there as the avatar they are in, and everything said in their
threads, by anyone, comes back on one stream as it is said. It has no server, keeps nothing and asks no model itself:
static files.

Built from [assistant-ui](https://www.assistant-ui.com) (MIT): its external
store runtime (`@assistant-ui/react`) from npm, fed from reins, and its chat
components from its registry. React, Vite, Tailwind.

## Ours and upstream's

| | |
|---|---|
| `src/App.tsx`, `src/main.tsx`, `index.html` | ours: the external store runtime wired to the world, the theme |
| `src/world.ts` | ours: the threads, their entries and the stream from reins, entries as messages and messages as threads' blocks |
| `src/ToolCard.tsx` | ours: an avatar's tool call and what the tool returned (a picture), one card inside its message |
| `src/Sidebar.tsx`, `src/Composer.tsx`, `src/Said.tsx` | ours: the sidebar (the picker on top, upstream's thread list, who is logged in at its foot); the composer's row (a call's buttons, who is writing, Stop) and what stands in the composer's place with nobody to write as; who said a message of the other side, and when |
| `src/Picker.tsx`, `src/who.ts`, `src/device.ts` | ours: who the person is on this device (the ghost or an avatar, each with who holds it, the radio of the picked one's brains), kept per device; this browser's device id |
| `src/Settings.tsx`, `src/avatars.ts`, `src/Models.tsx` | ours: the settings (the avatars, their profiles and bodies, a new one made from the "+" beside them), the avatars from reins and who holds each, the models offered |
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
`upstream.sh` may bring `@assistant-ui/react-ag-ui` back into
`package.json`: the face no longer uses it, so take it out again.

`src/upstream` is about 5,600 lines nobody here wrote: read it when the work
is about the UI's edge, not to learn the face.

## What it expects

| | |
|---|---|
| `GET /api/reins/me/threads`, `POST /api/reins/me/threads` | [reins](../../reins/CONTRACT.md): the threads the person's avatars are in, and a new one opened |
| `GET`, `POST /api/reins/threads/{id}/entries` | reins: a thread read page by page, and what the person says appended as their avatar |
| `GET /api/reins/me/stream?device=` | reins: server-sent events, each new entry of those threads (`entry`), the pieces of a reply being written (`writing`) and who holds one of the person's avatars now (`hold`); while it is open, this device is there |
| `POST /api/reins/subjects/{id}/take`, `/give-back`, `/hand`; `GET /api/reins/brains` | reins: an avatar's reins taken from this device, given back to nobody, handed to a brain (a profile `brains` lists) |
| `PATCH`, `DELETE /api/reins/threads/{id}` | reins: a chat renamed as the person's avatar, and deleted for good |
| `POST /api/reins/threads/{id}/stop` | reins: the reply a brain writes as one of the person's avatars, stopped |
| `GET /api/v1/models` | OpenAI's list of models, with `mode` and a speech model's `voices` beside OpenAI's fields; each mode's first is the agent's own. The preferences show in the settings only when this answers |
| `GET`, `POST /api/reins/me/subjects`, `PATCH /api/reins/subjects/{id}` | [reins](../../reins/CONTRACT.md): the avatars the person steers, a new one made, and a change of one's name, bio or preferences. The settings show only when this answers |
| `POST /api/v1/audio/transcriptions` | OpenAI's: a recording in, `{text}` out. The mic shows only when this is answered |
| `POST /api/v1/audio/speech` | OpenAI's: `{input}` in, a sound out. Read-aloud shows only when this is answered |
| `GET /api/v1/realtime` | OpenAI's Realtime API, a WebSocket: a call. The call button shows only when this is answered |
| `GET /api/v1/userinfo` | OpenID Connect's UserInfo claims (`sub`, `preferred_username`, `name`, `email`), and `end_session_endpoint` beside them: who is logged in, and where to log out. Shown only when this is answered |

Every request to reins names this browser (`X-Device-Id`, `?device=` on
the stream): a person holds an avatar's reins from a device
(`src/device.ts`, an id kept in `localStorage`).

A `401` from any of these reloads the page: whatever door stands in front gets
to ask the person to log in. The face knows no door of its own.

## Who is logged in

At the very foot of the sidebar (`src/Me.tsx`), as shadcn's sidebar blocks
have it: an avatar with initials, the name and the email, and a log-out
button that goes to `end_session_endpoint`. All of it comes from
`GET /api/v1/userinfo`, asked once when the page opens; whatever door
stands in front answers there (`gate` does, with its `userinfo.conf`),
and where nothing does, nothing is shown. assistant-ui has no element for
this; shadcn's sidebar menu and avatar, which upstream brings, make it.
Where reins answers, the row opens the settings.

## Settings: the avatars

The person's avatars are the subjects they steer, as reins answers at
`GET /api/reins/me/subjects` (`src/avatars.ts`): the first login gives
`<login>` and `<login>-assistant`. The settings (`src/Settings.tsx`) are
shadcn's "sidebar in a dialog" block (`sidebar-13`) made ours: the avatars
on the left, the one chosen on the right with a breadcrumb, a form of
shadcn's field, input and textarea; on a phone the avatars go above it.
assistant-ui has nothing for settings. The block itself is demo content, so
`upstream.sh` brings what it is built of (dialog, sidebar, breadcrumb, and
field for the form), not the block.

Each avatar shows its nick, which never changes, its name and its bio, and
its body, among what `GET /api/v1/models` offers: its **ear** (speech to
text), its **mouth** (text to speech) and one of the mouth's **voices**
(a mouth whose engine lists none says so, and speaks with its own).
Every avatar shows the same, whoever is in it: nothing in a nick says what
an avatar is for. Which model thinks is not the body's (below).

Save sends what changed (`PATCH /api/reins/subjects/{id}`): a name or bio,
and the preferences changed, merged by reins into the stored ones; a mouth
and its voice go together, and a new mouth brings its first voice. What is
saved is in the next request and survives a reload. A list's first is
shown when nothing is set: the agent's own. A refusal is said beside Save,
in reins' words. Which keys each preference is stored under is in reins'
contract. The subject's type is not read or shown.

## Who I am here: the picker, the ghost, the radio

The page is organised around who the person is on this device, not around
chats. A filter on the world by a query (a chat, a time) is the observer's
look; seeing *as someone* is stepping into their skin. The two never stand
in for each other (the repo's CLAUDE.md), so there is no filter by avatar:
being in an avatar is the picker.

**The picker** is the top of the sidebar, above New thread
(`src/Picker.tsx`): the **Ghost**, then every avatar the person steers,
each showing who holds its reins now (reins' CONTRACT.md, section 5):
"you, here", "you, on another device", another steward's name, "brain
<profile>", or "nobody". Shown to the person only: the characters never
see it. The pick is kept per device (`units.who` in `localStorage`, beside
the device id, `src/who.ts`): every tab of a browser is the same session;
another account is a private window or another browser. It is not in the
address: a link is shared, a session is not.

**The ghost**, the default and where every login lands, is the principal
without a body. It sees every thread of every avatar the person steers,
exactly what reins shows a principal, and can do nothing: no composer, no
New thread, no Stop, no rename or delete in a thread's menu. Every message
is someone else's: on the left, with the sender's avatar circle, name and
time (`src/Said.tsx`). The thread list is a filter by chat: one thread
picked is that thread shown.

**In an avatar** the page sees through it: for now its perception is the
threads it is a member of (the real perception filter is UNIT-70). Its own
messages are on the right, others' on the left with name and time. It
writes as it only while its reins are held here; otherwise there is no
composer, and in its place, in the page's flow, who holds it and how to
step in.

**The radio** under the picked avatar is who is in it: `none` (give back:
nobody holds it), `this device` (take: the composer comes and the page
writes as it), or one per brain profile reins lists (`GET /brains`; hand:
the page watches it answer, with Stop beside the composer's row while it
writes). Held by another device or another steward, that is said and
nothing is checked; picking an option still takes it over: stewards are
equal. A refusal is said under the radio, in reins' words.

**Leaving** an avatar (picking another, or the ghost) gives it back, to
nobody, when this device holds it; held by anyone else (a brain, another
device, another steward) it is left as it is. Nothing is returned to
whoever held it before. Nothing is taken on load: the person lands as the
ghost and steps in.

Changes come on the stream (`hold`): a person whose reins were taken
elsewhere sees the composer go at once; after the stream was away, the
avatars are read again. Leaving the page lets go after a while: reins lets
a hold lapse when the device's stream has been gone past its grace (2 min
by default).

On a phone (below 768 px) the sidebar is a sheet, opened from the button
at the top left; picking a thread closes it. Picker, radio, thread and
composer work at 375 px without scrolling sideways.

## Chats

Each chat is a thread in `threads`, read and written through reins
(`src/world.ts`), fed to assistant-ui's external store runtime
(`useExternalStoreRuntime`) and its thread list adapter. The page keeps
nothing: a reload reads it all again. The chat shown is in the address
(`/t/<thread id>`), so a reload stays in it; with none, or one not seen
from here (unknown, or not a thread of the avatar picked), the newest seen
is shown.

- **Acting as.** The page writes as the avatar the person is in, held here
  (above; `src/avatars.ts`). What the person says is appended as that
  avatar (`POST /api/reins/threads/{id}/entries`); a message is shown as
  the person's when its sender is the avatar they are in, and on the other
  side when it is anyone else's. A new chat is not opened until something
  is said in it: then a thread with that avatar and the first other one
  (`POST /api/reins/me/threads`).
- **Following.** One stream (`GET /api/reins/me/stream`, an `EventSource`)
  brings each entry of any of the person's threads as it is appended, by
  whoever: another tab, another device, the driver holding the other
  avatar's reins. It is opened before the threads are read, so nothing
  said in between is missed; an entry had twice is one `seq`. A dropped
  stream comes back from its last id by itself (`Last-Event-ID`), one that
  cannot (a 502) is opened again from it a few seconds later, and a 401
  reloads the page. A `reset` (the position is gone: the bus came back
  empty) reads everything again.
- **A reply being written** comes as `writing` pieces: shown as a message
  of the other side while it grows, its reasoning apart, and gone when its
  entry comes; beside the composer, "<name> is writing…" (`role=status`)
  while it does, from the moment its brain starts. Beside a brain holding
  one of the person's avatars, a **Stop** button in the composer's row (in
  the page's flow, never over the conversation) asks reins to stop it: an
  action on the brain. What it wrote so far comes back as an entry, a
  sentence cut off; nothing, when it had written nothing. A reply that failed is said there, in the writer's words,
  until the page is reloaded: it was never journaled. Composing is never held while someone writes.
- **The list.** A sidebar lists the chats, as assistant-ui's
  `threadlist-sidebar` does (shadcn's sidebar and upstream's thread list,
  put together in `src/Sidebar.tsx` without that element's own branding):
  a new one, search, switching. A chat is called by its name in `threads`,
  or the start of its first words. Its menu renames it, as the avatar the
  page acts as (an update entry everyone in it sees, the label changes on
  their pages without a reload), and deletes it for good, for everyone in
  it (reins' DELETE; their pages find it gone at their next read). There
  is no archive: `patches/04-no-archive.patch` takes it out of the menu.
  With nobody to act as (the ghost, an avatar held elsewhere) there is no
  New thread and no menu.

Collapsed by the button at the top left, a sheet on a phone.

## Which model, ear, mouth and voice

**The chat model** is not the page's: it is the brain holding the other
avatar's reins (a driver's profile, handed from the picker's radio), not
a property of any avatar's body. The page picks no model.

**Ear, mouth and voice** are the avatars' bodies (`src/avatars.ts`): the
page is the avatar picked and hears the first other one.

- **Ear** (mine, `ear`): the mic's transcription `model` (before the
  sound).
- **Mouth** and **voice** (the other's `mouth`, `voice`): read-aloud's
  `model` and `voice`.
- A call names all of them (below).

A preference not set is not sent: the agent's own. The agent's entries at
`GET /api/v1/models` (OpenAI's list; for the lab's mind, LiteLLM's groups)
say what each is for in `mode` (`chat` when not said,
`audio_transcription`, `audio_speech`) and a speech model's `voices`,
fields of its own beside OpenAI's; the settings offer each mode's list
with assistant-ui's model selector, without the runtime. No reins: no
settings, and the agent's own.

## Attachments

A picture, a text file or a PDF can go with a message (`src/attachments.ts`):
assistant-ui's own adapters for pictures (a `data:` URL) and text files (the
text, naming the file), and ours for a PDF (a file part). Into the journal
they go as `threads`' blocks (`src/world.ts`): a picture as an `image` block
whose `url` is its `data:` URL, with the bytes' `sha256`; a text file as the
text assistant-ui made of it (`<attachment name="…">…</attachment>`); a PDF
as a `file` block, url, sha256 and name. Read back, they are the message's
attachments again. Their bytes live inline in the journal until they are
kept elsewhere (artifact_store, a later task). A file over 10 MB is refused
when attached: the attachment shows an error, its reason when pointed at,
and nothing is sent while it is there.

## An avatar's tool calls

What a tool returns is the tool's; the avatar's act is the call (the
repo's CLAUDE.md). A brain's reply journals a `tool_call` and the
`tool_result` that answered it beside its words (drivers/model); the face
shows the pair as one tool card inside that avatar's message
(`src/ToolCard.tsx`, in upstream's `ToolFallback` slot): which tool
(`images · draw` for the mind's `images__draw`), the prompt it was asked
as it is, and what the tool returned — a picture or a sound on the card,
captioned "from" the tool, never among the avatar's words. The card's
trigger is assistant-ui's own (how the call went) and opens the arguments
and the tool's words. Cards stand as they are, each its own, instead of
upstream's folded "Used n tools" group (`ToolGroup`). Seen from inside an
avatar a brain drove, its tools' pictures are attachments named for the
tool. Nothing of a call shows while the reply is written: it comes with
the entry.

## Sound, turn by turn

The mic in the composer records until it is pressed again, then the
recording is transcribed whole and its text lands in the composer, to read
and send; a button on an answer reads it aloud (`src/voice.ts`). Both go to
OpenAI's audio API on the same origin, with the model and voice the
avatars prefer (above); none preferred, the endpoint picks. assistant-ui's own adapters for this are
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
was said, and names, when the call starts, my avatar's ear as
`audio.input.transcription.model`, the other's voice as
`audio.output.voice` (`<mouth>_<voice>`, `piper_irina`); no `chat_model`,
so a realtime model built of parts (`minds/realtime`) thinks with its own.
A realtime model built of parts answers with those; one that is a single
model ignores them. What each side said shows in the chat as a voice
conversation, the answer word by word, and stays there (`onVoiceTranscript`)
**for as long as the page is open: a call's transcripts are not journaled
yet** (UNIT-52), so they are not in the thread, not seen elsewhere, not read
by the driver, and gone on reload. While the call
goes: mute (nothing is sent), hang up. An interruption is taken from
`input_audio_buffer.speech_started` while an answer plays, or from the
answer's `response.done` before its `output_audio.done` (all a model behind
LiteLLM was seen to send, `contracts/edges.md`): the sound stops and the
model is told how much was heard (`conversation.item.truncate`,
`audio_end_ms`). The cut answer then shows what was heard of it, not what
was generated: once the server says it cut it
(`conversation.item.truncated`), the page asks for the item
(`conversation.item.retrieve`) and shows its transcript as the server now
holds it (our cascade keeps the heard words). A server that drops
`truncate` (LiteLLM's Gemini, `fake-realtime`) or empties the transcript
(OpenAI's) leaves the answer as generated, after 2 s at most. A close the page did not ask for, or the endpoint's
`error`, is said at the top of the page. Typed text does not go into a
call.

## Showcase

```bash
cd showcase
docker compose up -d --build
docker compose run --rm demo
```

Then http://localhost:8080. Behind the page are reins, subjects, threads and
a bus, and a front that lets everyone in as `alice` the way a door would.
Nobody answers there: what alice says is kept, comes back on the stream to
every tab she has open, and is there after a reload. The demo does the
same through the front with curl.

## Developing

`npm run dev` serves the page with hot reload and sends `/api` to the
showcase's front (`http://localhost:8080`), or to `API_URL`. `./check.sh`:
install, typecheck and build (upstream included: a type error there is news
too), lint (ours only), the manifest.

Light or dark follows the system's setting, and changes with it.

## Not yet

- Inviting someone else's avatar; their names (a sender not the person's
  shows as "someone").
- An avatar warehouse (create, rename, delete avatars), a merged feed of
  all threads with a time filter, handing over the post: their own tasks.
- A user message's Edit button shows, but nothing edits an entry: a
  correction is a new one.
- The bundle is 1.6 MB (460 KB gzipped), mostly what assistant-ui brings for
  markdown, attachments and tools.
