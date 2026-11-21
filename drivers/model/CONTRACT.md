# drivers/model — contract

A chat model holding a subject's reins. A subject is a character in
`subjects`, whoever drives it; a person drives theirs through `reins`, and a
driver is the other kind of brain. It can be any of its **profiles**
(`PROFILES`, `<name>=<chat model>,…`): a brain in the broad sense, who
moves into an avatar. Each profile carries only its chat model now; the
shape leaves room for choices by context later (a realtime model in a
call, vision or text only, agent work). Which model thinks for a subject
is the brain's, not the subject's body.

Which subjects it holds, and as which profile, is `reins`' to say: a
steward hands an avatar to a profile there, and takes it back (reins'
CONTRACT.md, section 5). The subject's context is one whichever brain is in
it: the prompt is built from the journal, so another profile changes only
who thinks.

It keeps nothing: what was said is `threads`', what is being written goes by
on the bus and is gone.

## 1. Whom it holds

At boot, on each `reins.hold.changed` on `reins_events` and every 30 s (an
event missed), it reads reins' inner `GET /holds` (`REINS_URL`, never
through the door): every subject a brain holds, by profile. It holds those
of its own profiles; another driver's are not its. The event only wakes:
the truth is read back.

- A subject **gained** is caught up with: in each thread it is in
  (`?member=`), where the last thing said is someone else's and at most
  `CATCHUP` old (2 min), it answers (said while nobody held it); and where
  it opened the thread and nothing is said yet, it greets (below).
- A subject **lost** (taken back, handed to another, given to nobody, or
  let go by the driver itself after a failed reply, section 4): a
  reply being written as it is cut, and **nothing is said**: the subject
  is another's now (`driver.reply.stopped`, `why: taken`). A reply that
  finished meanwhile is not said either.
- `GET /profiles` says its profiles, `[{name, model}]`, for reins to list
  (`GET /brains`).

## 2. When it answers

It reads `threads_events` through a consumer group of its own (`GROUP`,
`drivers-model`), so a restart goes on where it stopped. For each
`threads.entry.appended` it is woken, for each subject it holds, when:

- the entry is a `create` (something said or done; joins, leaves and renames
  are not talk), and
- its sender is not that subject, and
- that subject is a member of the thread (`GET /threads/{id}`).

Woken, it reads the truth back: the thread's last `TAIL` entries
(`?tail=true`). When the last thing said there is the subject's own, there
is nothing to answer and nothing is done; so a wake-up handled twice (after a
restart, or two arriving while one is answered) costs a read, never a second
answer. Answers in one thread for one subject come one at a time; what is
said while one is written is answered after it, from the tail as it is
then. A wake-up is acknowledged once what it woke is answered.

A wake-up for a subject it does not hold is acknowledged unanswered; what
was said to it lately is answered when it is taken (section 1). Older
words are left as they are: a restart does not answer a thread anew.

**It speaks first** in a thread its subject opened where nothing is said
yet (no `create` in the thread): woken by someone else's `join` there, or
on taking the subject (section 1). Reins opens a principal's first thread
so: by `<login>-assistant`, with `<login>` in it. The prompt is then one
cue, not journaled: nobody has said anything in this conversation it
opened, who is there, greet them. What it says is journaled as any reply.

Two brains holding two subjects of one thread would answer each other
without end: nothing guards against it yet (UNIT-70).

## 3. The prompt

Built from the tail, the first filter being identity's: `threads` knows no
roles, they are computed here.

- The subject's own `create` entries are the **assistant's**, in the order
  they hold: their text blocks (a sound by its transcript), joined; a
  `tool_call` answered by a `tool_result` in the same entry as the
  assistant's tool call, and the result as the tool's message in words: its
  `text`, each of its `media` named (`[an image, image/png]`). Its own
  pictures and sounds from tools never go back to the model as themselves.
- Everyone else's are the **user's**, with **who said it** in the words: the
  first text part begins `<name>: ` (the subject's name in `subjects`, its
  nick when it has none). A picture is an AG-UI `image` part and a `file`
  (a PDF) a `document` part, their bytes inline from a `data:` URL
  (`source.type: data`), else by URL; an attached text file's text
  (`<attachment name="…">`) is a text part as it is.
- Joins, leaves and renames are left out. No system prompt of its own: the
  mind's is first.

## 4. The answer

The prompt goes to `minds/assistant` as an ordinary AG-UI run (`POST
MIND_URL`, the protocol's Go SDK as the client): `threadId` the thread's
id, `runId` a new reply id, `messages` the prompt,
`forwardedProps.modelName` the chat model of the profile holding the
subject. The mind
stays stateless; the driver names itself to it as `X-Principal-Id:
drivers/model` (it is reached inside the stack, not through the door).

- Each `TEXT_MESSAGE_CONTENT` and `REASONING_MESSAGE_CONTENT` is published as
  it comes (section 5). Tool calls are read, not published.
- `RUN_FINISHED`: the reply is said in the thread as the subject, **the
  avatar's act in the order it was made**: one `create` entry (`POST
  /threads/{id}/entries`) with a `text` block per text message that said
  words, a `tool_call` per call the mind made (`TOOL_CALL_START`, its
  `TOOL_CALL_ARGS` joined: a JSON object, else `{"arguments": "<as it
  came>"}`), and after it the `tool_result` the tool gave
  (`TOOL_CALL_RESULT`). What a tool returns is the tool's, not the avatar's
  words: the result's `text` is the tool's own text blocks and its `media`
  the tool's pictures and sounds, read from the event's `rawEvent` (MCP's
  `CallToolResult`, contracts/edges.md), the bytes as a `data:` URL with
  their sha256 until they have a store (UNIT-68); without a `rawEvent`,
  `text` is the event's `content`. A call no result answered is left out.
  Reasoning is not journaled.
- `RUN_ERROR`, a mind that does not answer or breaks off, or a run that said
  and did nothing: **nothing is journaled** — the journal holds what was said. The
  failure is published (`driver.reply.failed`, the mind's `code` and
  `message`) and logged, and **the subject is given back** (fail fast): the
  driver lets go of its own hold on reins' inner `POST
  /holds/{id}/release` as the profile it answered with, and nobody holds
  the avatar (`reins.hold.changed`, holder `""`); its other replies being
  written are cut as taken. Nothing is tried again here; re-attaching is a
  steward's, by taking or handing it at reins. When someone took or handed
  it on meanwhile, reins leaves it with them. When reins cannot be reached
  the hold stays as reins says it, and that is logged.
- The journal refusing the reply (`driver.reply.failed`, code `not_said`)
  is published the same way but is not the brain failing: the subject is
  kept. The brain did its part; the refusal is the world's (the thread
  gone, the subject no longer in it, threads down), and an avatar put on
  standby for it would answer nowhere else either.
- **Stopped**: a `reins.reply.stop` for the thread and the subject (a
  steward asked reins) cancels the run. What was written so far is said as
  it stands, one `text` block, when there is any text (those watching saw
  it: a sentence cut off); when there is none, nothing is journaled.
  `driver.reply.stopped`, `why: stopped`, with `seq` when it was said.
  What came meanwhile is not answered on its own: the next word wakes it.

## 5. Events

Published on the Redis stream `driver_events`, in the envelope of
[`contracts/events.md`](../../contracts/events.md), source `/drivers/model`,
`subject` the thread's id. Ephemeral: they matter while a reply is written,
and the stream is capped (about 10 000). `reins` relays them to the
people who see the thread (its `GET /me/stream`, event `writing`).

| `type` | `data` |
|---|---|
| `driver.reply.started` | `thread`, `sender` (the subject), `reply`: a run begun, nothing of it yet |
| `driver.reply.delta` | `thread`, `sender`, `reply`, `part` (`text` or `reasoning`), `text` (the piece) |
| `driver.reply.failed` | `thread`, `sender`, `reply`, `code`, `message` |
| `driver.reply.finished` | `thread`, `sender`, `reply`, `seq` (the entry said) |
| `driver.reply.stopped` | `thread`, `sender`, `reply`, `why` (`stopped`: asked; `taken`: the reins went to another), `seq` when what was written was said |

`reply` is one reply's id, the run's `runId`; a reader puts its pieces
together by it, and drops them when the entry comes (or `finished`,
`stopped`).

## 6. Boundaries

- No database. Its routes: `GET /profiles`, `/health` and `/ready` (the bus
  answers; the subjects it holds, by profile).
- It writes into `threads` only as a subject one of its profiles holds, as
  reins says, and only `create` entries.
- It does not check who may drive a subject: who holds what is `reins`',
  and it follows it. The one change it makes there is letting go of its
  own hold when a reply fails.
