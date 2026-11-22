# units

Self-sufficient services and libraries, each useful on its own and assembled
into applications. See [README.md](README.md) for the list.

## Work comes from the board

Tasks for this repo live on the task board, project **`UNIT`**, reached with
the `/tasks` skill in `.claude/skills/tasks/` — local, gitignored, because it
is wired to one deployment of the board. Not a TODO file, not a BACKLOG.md.
Pick up, file, move and close work there; something noticed and not fixed now
becomes a task with acceptance criteria.

## Layout

- One unit per directory, its own `go.mod`. No cross-unit Go imports except
  `shared/`. A unit must build, test and run without the others.
- A unit that is a service carries `unit.yaml` (what it provides, consumes and
  needs as config). `scripts/check_unit.py` holds the manifest to the code.
- Every unit has `check.sh` (build, vet, gofmt, lint if present, tests, swagger
  freshness, manifest). Services have a `showcase/` (`compose.yaml` +
  `demo.sh`) that shows them working on their own; `engines/showcase` covers
  the engines together.
- Generated code from a standard's own source stays as it comes, with our
  changes as patches on top: `faces/web_chat/src/upstream` (see faces/web_chat/README.md).
  Never edit it by hand, and do not read it to learn the unit — only when
  the work is about that edge.
- Events: CloudEvents on a Redis stream, per [contracts/events.md](contracts/events.md).
- `stacks/<name>/` assembles units into something that runs: `compose.yaml`,
  `.env.example` (a local run works from it as is; a deployment changes only
  `.env`), a `check.sh`. No unit depends on a stack.
- `scripts/` is the repo's own scripts (manifest and secrets checks, the
  pre-push hook, address moves, evals). `tools/` is the family of tools
  avatars call (MCP servers), named as the AI world names them.

## Words for what an avatar has

| word | what it is | where it lives | taken away by | e.g. |
|---|---|---|---|---|
| sense | an organ through which the world reaches the avatar | its body | a blindfold | ear (`senses/hearing`); eye (vision: inside the model today, a describer is its prosthesis) |
| ability | an organ through which the avatar acts on the world | its body | tied hands, a gag | mouth (`abilities/speaking`); hands = calling tools |
| tool | a thing in the world the hands use | the world; access is a per-avatar switch | the brush taken | `tools/images`, a calculator, the task board |
| skill | knowing how to use an ability and a tool well | the mind, memory: learned, passed on | forgetting, never learning | how to prompt a picture, how to review code |
| brain | raw quality of thinking ("talent") | whoever holds the reins | a brain swap | a driver's profile, a person |
| engine | machinery behind an organ or a tool, unseen by characters | infrastructure | — | FLUX on Cloudflare, whisper, silero |
| device | where the body meets the real world | the world | — | a phone, a mic, a speaker, a LiveKit room |

Drawing is not an ability: it is hands, a tool and a skill. Whoever cannot
draw makes a picture with a generator; a bound artist loses the ability and
keeps the skill. The body is the same set on every avatar; what differs is
its tools, skills and brain. What a tool returns is the tool's, not the
avatar's: the avatar's act is the call.

## Looking at the world is not perceiving it

Two things that look alike and never stand in for each other:

| | what it is | how it is reached |
|---|---|---|
| a query | the observer's look at reality, filtered (by chat, by time, …) | a filter on what is there |
| perception | seeing *as someone*: what reaches them, as it reaches them (a blind avatar gets "one new message", not the picture) | only by stepping into their skin |

A filter by avatar is neither, and is not offered: being in an avatar is
stepping into it (the face's picker), not narrowing a query. That is why
the ghost (the principal with no body) lists threads by chat but has no
filter by avatar.

## Done means

- The touched unit's `check.sh` passes — run it from the unit's directory.
  Run the `check.sh` of units whose `unit.yaml` consumes what you changed too.
- Behaviour changes are seen through the showcase when it can show them.
- README and `unit.yaml` match the code; a new unit gets a row in the root
  README table.

## Heavy runs belong to the main thread

A stack (`stacks/*/check.sh`, a second lab, a showcase that builds many
images, anything on the GPU box) costs minutes, machine and tokens. It runs
once, in the conversation the person is in, on the code that will be
merged, so they can see it and take part.

- A delegated piece of work (a subagent, a session in a worktree) runs only
  the `check.sh` of the units it touched and of their consumers, and says in
  its report which stack runs its change needs. It does not bring up a stack
  unless its brief asks for one.
- The main thread merges the pieces, then runs the stack check once on the
  merged code.
- Where a person does something faster (a look in the browser, a click, a
  key, a VPN), ask them rather than build a workaround.

## Commits

- One unit (or one concern) per commit. Subject: `<unit path>: <what it is or
  does now>`, lowercase, no trailing period, e.g.
  `abilities/speaking: a mouth, text in, audio out, what was spoken reported`.
- Body: prose, why and what changed in behaviour, not a file list. Name the
  board handle (`UNIT-5`) when there is one.
- This repository is public: no hosts, addresses, tokens or deployment
  details in tracked files. `scripts/check_secrets.sh` holds the repo to it:
  gitleaks with `.gitleaks.toml` (its defaults plus passwords in URLs) over
  every commit and the index, and every value of the local gitignored env
  files looked for literally, a value equal to a tracked `.env.example` line
  excepted. A hit names file and rule or variable, never the value. It runs
  before every push once a clone has `git config core.hooksPath scripts/hooks`.

## Local only

- `_ignored/` and `.claude/` are gitignored. `_ignored/` is scratch (eval
  results, probes); the board's token sits beside the `/tasks` skill in
  `.claude/skills/tasks/`. Never print a secret file's contents.
