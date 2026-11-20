# drivers/model — contract

A chat model holding a subject's reins. A subject is a character in
`subjects`, whoever drives it; a person drives theirs through `reins`, and a
driver is the other kind of brain: it holds a subject with a chat model.
Which model thinks for a subject is the driver's, not the subject's body.
Which subjects it holds, and with which model, is configuration for now
(`HOLDS`); handing the reins between a person and a driver ("act or
watch") comes later.

It keeps nothing: what was said is `threads`', what is being written goes by
on the bus and is gone.

## 1. When it answers

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

A subject it cannot find yet (not made yet: `<login>-assistant` is made at
the login's first visit) is looked for again every 5 s; until then it holds
nothing for it, and the wake-ups for it are acknowledged unanswered. So,
on taking a subject's reins, it answers in each thread the subject is in
(`?member=`) where the last thing said is someone else's and at most
`CATCHUP` old (2 min): what was said while it looked. Older words are left
as they are: a restart does not answer a thread anew.

Two drivers holding two subjects of one thread would answer each other
without end: nothing guards against it yet.

## 2. The prompt

Built from the tail, the first filter being identity's: `threads` knows no
roles, they are computed here.

- The subject's own `create` entries are the **assistant's**: their text
  blocks (a sound by its transcript), joined.
- Everyone else's are the **user's**, with **who said it** in the words: the
  first text part begins `<name>: ` (the subject's name in `subjects`, its
  nick when it has none). A picture is an AG-UI `image` part and a `file`
  (a PDF) a `document` part, their bytes inline from a `data:` URL
  (`source.type: data`), else by URL; an attached text file's text
  (`<attachment name="…">`) is a text part as it is.
- Joins, leaves and renames are left out. No system prompt of its own: the
  mind's is first.

## 3. The answer

The prompt goes to `minds/assistant` as an ordinary AG-UI run (`POST
MIND_URL`, the protocol's Go SDK as the client): `threadId` the thread's
id, `runId` a new reply id, `messages` the prompt,
`forwardedProps.modelName` the model it holds the subject with. The mind
stays stateless; the driver names itself to it as `X-Principal-Id:
drivers/model` (it is reached inside the stack, not through the door).

- Each `TEXT_MESSAGE_CONTENT` and `REASONING_MESSAGE_CONTENT` is published as
  it comes (section 4). Tools are the mind's: their calls and results are not
  read here, only the words said after them.
- `RUN_FINISHED`: the reply is said in the thread as the subject: one
  `create` entry (`POST /threads/{id}/entries`), one `text` block per text
  message of the run, in order. Reasoning is not journaled.
- `RUN_ERROR`, a mind that does not answer or breaks off, or a run that said
  no words: **nothing is journaled** — the journal holds what was said. The
  failure is published (`driver.reply.failed`, the mind's `code` and
  `message`) and logged.

## 4. Events

Published on the Redis stream `driver_events`, in the envelope of
[`contracts/events.md`](../../contracts/events.md), source `/drivers/model`,
`subject` the thread's id. Ephemeral: they matter while a reply is written,
and the stream is capped (about 10 000). `reins` relays them to the
people who see the thread (its `GET /me/stream`, event `writing`).

| `type` | `data` |
|---|---|
| `driver.reply.delta` | `thread`, `sender` (the subject), `reply`, `part` (`text` or `reasoning`), `text` (the piece) |
| `driver.reply.failed` | `thread`, `sender`, `reply`, `code`, `message` |
| `driver.reply.finished` | `thread`, `sender`, `reply`, `seq` (the entry said) |

`reply` is one reply's id, the run's `runId`; a reader puts its pieces
together by it, and drops them when the entry comes (or `finished`).

## 5. Boundaries

- No database and no routes of its own but `/health` and `/ready` (the bus
  answers; the subjects it holds, with their models).
- It writes into `threads` only as a subject it holds, and only `create`
  entries.
- It does not check who may drive a subject: holding is configuration, not
  stewardship (that is `reins`').
