# job_runner

An isolated machine for agents: files in, a script runs, files out, answered
when the script has ended. Each run is a fresh container under gVisor, with no
capabilities, `no_new_privs`, limits on memory, processes and CPU, and no
network unless the call names one. What it promises is `CONTRACT.md`; this
file is for working inside it.

Door: MCP over streamable HTTP at `/` on `:8080` — `run`, `peek`, `list` —
plus `GET /health`. No HTTP API and no console: it is meant to be asked by
programs and models, and `peek`/`list` are how they look.

## Where things are

| Looking for | Go there |
|---|---|
| a run: start, follow, kill at the limit, the answer | `internal/run/runner.go` |
| inputs, outputs, the log, the change report | `internal/run/transfer.go` |
| peek and list | `internal/run/peek.go` |
| the tools and the text of an answer | `internal/run/mcp.go` |
| killing past the limit, finishing after a restart, dropping past keep | `internal/run/watch.go` |
| the dozen Engine API calls, over the daemon's unix socket | `internal/engine/client.go` |
| a fake daemon for the tests | `internal/engine/enginetest/` |
| the daemon: Docker in Docker, gVisor, the egress rule | `dind/` |
| env, wiring | `cmd/app/main.go` |

## How a run is held

- **A run is a container named `jr-<key>`**, its facts in labels
  (`io.github.jurasikasan.units.job_runner.*`: deadline, keep, image and
  network names, what came in and where things go out) and `/work` in a volume
  of its own. Nothing else is kept: a restart of this process finds every run
  where it left it. The label names are stored data — renaming one strands the
  runs of the version before.
- **`/work` belongs to uid 1000** because a short root container of the first
  image chowns the fresh volume first; the inputs then go in as a tar through
  the Engine API (`PUT /containers/{id}/archive`), owned by 1000, before the
  container starts.
- **The answer is put together once** when the run has ended — the change
  report (a tar of `/work` read back and compared with the inputs), the
  outputs (each read back the same way and sent with PUT), the log — by the
  flight following the key, or by the sweep for a run that ended while this
  process was down. A restart halfway sends them again; PUT is idempotent.
- **Secrets** are masked in everything that leaves: the answer, `peek`, the
  log. A transfer's secret is sent only to `TRANSFER_URLS`, which is what keeps
  a run's caller from mailing a token to an address of its choosing.
- **No TTY**: a script sees a pipe, so tools print no colours and no `\r\n`;
  the log comes in frames, which `engine.Demux` takes apart.

## The daemon

`dind/Dockerfile` is `docker:dind` with gVisor's release (pinned, checked by
its sha512) beside it, registered as `runsc`. `dind-egress.sh` starts dockerd
on a unix socket only, then inserts the rule that forwarded traffic from a run
may not reach a private, link-local or CGNAT address; if it cannot, it stops
dockerd and the container exits, so job_runner, healthy-gated on it, never
starts a run without it. The daemon container is privileged — that is the
layer under gVisor, not the one a run sees.

## Check

```bash
bash check.sh                                              # build, vet, fmt, lint, tests, manifest
cd showcase && docker compose up --build --abort-on-container-exit --exit-code-from demo
```

The showcase starts the daemon, job_runner and a small file server, then
`demo.py` calls the tools as an agent would: a run, a peek mid-run, the same
key twice, files in and out with the token kept outside, the networks, gVisor
and the limits, a masked secret and the log of a failed run, a run stopped by
its limit. About two minutes, one of them waiting out a limit.
