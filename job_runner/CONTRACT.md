# job_runner — contract

Status: draft for v0.0.0.

## 1. What it is

An isolated machine for agents: files in, a script runs, files out, and the
answer comes when the script has ended. One run per call, in a fresh container
under gVisor, in a folder that holds only what the call put there. Nothing is
shared between runs. A caller that wants phases (prepare, work, check) makes
one run per phase, each with the network it needs.

It is built to be called by programs and models, not watched by people: what a
run is doing is asked with `peek`, not looked up on a server.

## 2. Depends on

Its own Docker daemon with gVisor (`runsc`) registered as a runtime. The unit
ships that daemon as an image (`dind/`): `docker:dind`, gVisor, and the egress
rule (section 5). The box needs Docker and nothing else.

| Variable | Required | Meaning |
|---|---|---|
| `DOCKER_HOST` | no, default `unix:///var/run/dind/docker.sock` | the daemon runs are started in |
| `IMAGES` | yes | the images a run may name, `name=reference,…` (`shell=bash:5.2,agent=registry/x@sha256:…`); the first is the default. A run cannot name anything else |
| `NETWORKS` | no | networks besides `none` and `internet` a run may name, `name=network,…`: networks the deployer made inside the daemon |
| `TRANSFER_URLS` | no | the prefixes an input, output or log URL must start with, comma-separated (`http://store:8094/v1/`). None: no transfers. A transfer's secret goes only to these |
| `MAX_KEEP` | no, default `4h` | how long an answer nobody has taken waits at most |
| `RUN_MEMORY` | no, default `2g` | memory of one run; past it the run is killed |
| `RUN_PIDS` | no, default `512` | processes of one run |
| `RUN_CPUS` | no, default `2` | CPUs of one run |
| `SECRET_<NAME>` | no | a value a run or a transfer may use by name: `SECRET_STORE_TOKEN` is the secret `STORE_TOKEN` |
| `HELPERS_DIR` | no | a directory, as the daemon sees it, mounted read-only at `/opt/helpers`, first on `PATH` |
| `PORT` | no, default `8080` | |

## 3. Interface

MCP over streamable HTTP at `/`, plus `GET /health`. Three tools.

### `run` — answers when the run has ended

| Argument | |
|---|---|
| `key` | the caller's name for the run (section 4) |
| `script` | run by bash, or by sh where the image has no bash |
| `image` | a name from `IMAGES`; the first if left out |
| `network` | `none` (default), `internet`, or a name from `NETWORKS` |
| `inputs` | `[{url, to, secret?}]`: each `url` fetched with GET into `/work/<to>` before the script starts, owned by the run's user; `secret` names a secret sent as `Authorization: Bearer`. A failed fetch fails the call, nothing runs |
| `outputs` | `[{from, url, secret?}]`: after the script, however it ended, each file `/work/<from>` sent with PUT to `url`. A missing file or a failed PUT is a line in the answer, not an error |
| `log` | `{url, secret?}`: after the run, however it ended (a failure, its limit), the whole output and the answer sent with PUT to `url`. For a run that must be read after its keep is over. A failed PUT is a line in the answer |
| `env` | `{NAME: value}`: environment of the script. Not secret |
| `secrets` | `[NAME, …]`: secrets put in the script's environment. Prefer `secret` on a transfer: then the value never enters the run |
| `limit_minutes` | how long the run may take, the image's pull and the inputs included; 30 if left out, at most 120. Outputs and the log have 10 minutes of their own after it |
| `keep_minutes` | how long the answer waits if the caller is gone; `MAX_KEEP` if left out, never longer |

`url`s must start with one of `TRANSFER_URLS`; paths are relative to
`/work` and stay under it.

The answer, as text; its first line is fixed, callers may parse it:

```
exit code 1 after 1m3.2s            (or: stopped by its limit after 30m0s)

Changes: +1 ~1 -1
  + out/result.bundle 1204
  ~ edit 6
  - gone 1

Outputs:
  out/result.bundle -> PUT 201
  out/change.patch -> missing

Log: PUT 201

Output:
<the last 200 lines, at most 16 KB>
```

Changes compare `/work` after the script with the inputs it started with:
added, changed (size or modification time), removed; the text names the first
20, the counts are of all. As `structuredContent`: `{exit_code,
stopped_by_limit, duration_ms, changes: {added, changed, removed: [{path,
size}]}, outputs: [{from, status}], log, notes, output_tail}`; `exit_code` is
null when the run was stopped.

Outputs and the log are sent once the run has ended, whether anyone waits or
not. A restart of job_runner in between may send them again: the same bytes to
the same URL.

### `peek(key, lines?)` — answers at once

`{state, started_at, elapsed_ms, output_tail, answer?}`; `state` is `running`,
`ended` (with `run`'s answer once its outputs are sent) or `unknown`. `lines` is how much of the
output, 50 if left out, at most 2000. Never starts or waits for anything.

### `list()` — answers at once

The runs this instance holds, running and ended within their keep:
`[{key, state, image, network, started_at, elapsed_ms}]`.

Inside a run: `/work` is the working directory and holds only the inputs;
uid 1000; no capabilities, `no_new_privs`; the limits of section 2; gVisor's
kernel, not the host's; no socket, no mount but `/work` and `/opt/helpers`.

## 4. Identifiers

- `key`: `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`, chosen by the caller, meaning
  nothing to job_runner.
- One run per key. A `run` with the key of a run still going starts nothing:
  it waits for that run and gets its answer, whatever its own arguments. A
  `run` with the key of a run that ended gets that answer too, until its
  `keep_minutes` have passed since it ended; then the key is free.

## 5. Guarantees and limits

- **The caller leaving does not stop the run**; the answer waits for the next
  `run` or `peek` with the key for `keep_minutes`.
- **The limit is the run's**: killed past it whether anyone waits or not.
- **Nothing is kept in the process.** A run's state is its container; a
  restart loses no run and no answer.
- **Networks.** `none`: no interface but loopback. `internet`: public
  addresses only — the daemon refuses every private, link-local and CGNAT
  range, the stack, the LAN and the host included. The same rule holds a
  deployer's network: it can narrow what a run reaches, never widen it.
- **Secrets are masked, not hidden.** Every occurrence of a secret's value in
  the answer and in `peek` reads `***`. A script given a secret can always
  send it elsewhere; a transfer's `secret` never enters the run.

### Threat model

A run is untrusted: it may run what a model wrote, from text it was injected
with. Covered: reaching the host's kernel (gVisor), the host and its LAN
(egress rule or no network), other runs (a container and a folder each),
secrets it was not given, the box's memory and process table (limits).
Not covered: what a run does on the internet with what it was given; an escape
from gVisor and then from the privileged daemon container; a malicious image
named in `IMAGES` — the deployer's choice.

## 6. Errors

| Answer | Means | Caller does |
|---|---|---|
| tool error: `key` breaks its alphabet, an unknown image, network or secret, a URL outside `TRANSFER_URLS`, a path outside `/work`, `limit_minutes` out of range | the call is wrong | fix it |
| tool error: an input could not be fetched | nothing ran | check the url |
| tool error: the daemon is unavailable | nothing ran | retry later |
| `stopped by its limit` | out of time | a bigger limit or a smaller script |
| exit code ≠ 0 | the script failed | the script's business |

## 7. Check

`showcase/demo.sh` against a running instance:

1. `run(key=c1, script="echo hello")` → exit code 0, `hello`.
2. `run(c2, "sleep 20; echo done")`, and while it runs `peek(c2)` → `running`,
   then `run(c2, …)` again → the same answer; `list()` shows c2.
3. An input fetched into `/work`, an output sent back, a missing output named.
4. `network=none`: no address answers; `internet`: a public address answers, a
   private one does not.
5. The kernel is gVisor's; a fork loop past `RUN_PIDS` and an allocation past
   `RUN_MEMORY` end the run, not the box.
6. A secret: masked in the answer; on a transfer, absent from the run's
   environment.
7. `limit_minutes=1` on `sleep 600` → stopped by its limit.
