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
  changes as patches on top. Never edit it by hand, and do not read it to
  learn the unit — only when the work is about that edge.
- Events: CloudEvents on a Redis stream, per [contracts/events.md](contracts/events.md).
- Nothing here assembles units into an application: that is done outside
  this repository, from the units' images. No unit depends on one.
- `scripts/` is the repo's own scripts (manifest and secrets checks, the
  pre-push hook, address moves, evals). `tools/` is the family of tools
  agents call (MCP servers), named as the AI world names them.

## Done means

- The touched unit's `check.sh` passes — run it from the unit's directory.
  Run the `check.sh` of units whose `unit.yaml` consumes what you changed too.
- Behaviour changes are seen through the showcase when it can show them.
- README and `unit.yaml` match the code; a new unit gets a row in the root
  README table.

## Heavy runs belong to the main thread

A heavy run (a showcase that builds many images, anything on the GPU box) costs minutes, machine and tokens. It runs
once, in the conversation the person is in, on the code that will be
merged, so they can see it and take part.

- A delegated piece of work (a subagent, a session in a worktree) runs only
  the `check.sh` of the units it touched and of their consumers, and says in
  its report which heavy runs its change needs. It does not start one
  unless its brief asks for it.
- The main thread merges the pieces, then runs the heavy check once on the
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
