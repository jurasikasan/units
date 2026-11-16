# units

Self-sufficient services and libraries, each useful on its own and assembled
into applications elsewhere (mostly bot, `../bot`). See [README.md](README.md)
for the list.

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
  needs as config). `tools/check_unit.py` holds the manifest to the code.
- Every unit has `check.sh` (build, vet, gofmt, lint if present, tests, swagger
  freshness, manifest). Services have a `showcase/` (`compose.yaml` +
  `demo.sh`) that shows them working on their own; `engines/showcase` covers
  the engines together.
- Events: CloudEvents on a Redis stream, per [contracts/events.md](contracts/events.md).

## Done means

- The touched unit's `check.sh` passes — run it from the unit's directory.
  Run the `check.sh` of units whose `unit.yaml` consumes what you changed too.
- Behaviour changes are seen through the showcase when it can show them.
- README and `unit.yaml` match the code; a new unit gets a row in the root
  README table.

## Commits

- One unit (or one concern) per commit. Subject: `<unit path>: <what it is or
  does now>`, lowercase, no trailing period, e.g.
  `abilities/speaking: a mouth, text in, audio out, what was spoken reported`.
- Body: prose, why and what changed in behaviour, not a file list. Name the
  board handle (`UNIT-5`) when there is one.
- This repository is public: no hosts, addresses, tokens or deployment
  details in tracked files.

## Local only

- `_ignored/` and `.claude/` are gitignored. `_ignored/` holds secrets
  (`tasks-token.env`) and scratch; never print its contents.
