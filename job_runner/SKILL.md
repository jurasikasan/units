---
name: job-runner
description: Run a script in a fresh, isolated machine (container under gVisor, no network unless asked) through the job_runner MCP server, with files passed in and out by URL. Use it to run a project's tests or build in a clean environment, to run code you would rather not run on this machine, or for long jobs that should not hold your session. Covers keys, inputs and outputs, networks, long runs and timeouts, and the pattern "repository in, report out".
---

# job_runner

Tools: `run`, `peek`, `stop`, `list` on the MCP server `job_runner`. If they are not
there, it isn't set up yet: follow [AGENTS.md](AGENTS.md) (in the units
repository, `job_runner/AGENTS.md`).

You need two file prefixes besides the server. Look for them in the
project's `AGENTS.md`/`CLAUDE.md`, or ask:
- **outside**: where you PUT and GET files (`curl -T`), with the same Bearer
  token as the server when remote;
- **inside**: the same files as job_runner reaches them. Only this goes into
  `url` in `inputs` and `outputs`. A wrong one is refused with
  `not under <inside prefix>`.

## When to use it

- Run a project's tests or build in a clean machine, not this one. It shows
  what a fresh checkout does, and it keeps whatever the code does off this
  machine.
- Run code or tools you don't trust: a dependency's install script, a
  generated program, a repository you have just cloned.
- A job that takes long. The run goes on whether or not you wait.

Don't use it for what needs this machine's state (local services, your
credentials, a GUI), or for anything interactive. A run has no TTY and no
stdin.

## What a run is

- **One call, one fresh machine.** `/work` holds only your `inputs` and is
  the working directory. Everything is gone when the run ends: nothing is kept
  between runs, not even caches. What you need later goes out through
  `outputs`.
- **uid 1000, no writable home.** Tools that write to `~` (pip, npm, go, git)
  fail with `Permission denied: '/.local'` unless you set
  `"env": {"HOME": "/tmp/home"}` and `mkdir -p $HOME` first. Under `/tmp`
  it also stays out of the change report.
- **No network** unless you pass `"network": "internet"`. Installing
  dependencies needs it; running the tests usually doesn't.
- **Images** are a fixed list chosen by whoever runs the server. Leave
  `image` out for the default, or name one. An unknown name is answered with
  the list.
- **Limit**: `limit_minutes`, 30 if left out, at most 120. It includes the
  image pull and the inputs. Past it the run is killed. Set it close to what
  the job needs. `stop` with the key ends a run sooner.
- **Answer**: the first line is fixed. It is `exit code N after T`,
  `stopped by its limit after T`, or `stopped by its caller after T`. Then
  come `Changes` (what changed in `/work`), `Outputs` (one line per file,
  e.g. `out/report.txt -> PUT 201`) and the last 200 lines of output (16 KB
  at most). Write anything longer to a file and send it out.

## Keys

`key` is your name for the run: `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`, unique
per attempt, saying what it is (`myproj-tests-3f2a1c`, `myproj-deps-2`).

- **One key, one run.** Calling `run` again with the key of a run that is
  going or has ended doesn't start anything. You get that run's answer,
  whatever arguments you pass this time.
- So a **new attempt needs a new key**: after changing the script, the
  inputs or the code.
- And a **retry of the same attempt keeps the key**: that is how you get
  back to it.

## Long runs and timeouts

- **Print progress to stdout.** `peek` shows only stdout and stderr. A tool
  that writes progress to a file shows nothing until the end, so `tail -f`
  that file in the background or pipe it through `tee`.
- **If your `run` call times out** ("The operation timed out", a client
  limit), the run has *not* stopped. Do not start it again under a new key.
  Call `peek` with the same key: `running` with the latest output, or
  `ended` with the full answer. `run` with the same key again also waits for
  it.
- **To avoid holding your session**, start the run, and if your client lets
  you, do other work and `peek` now and then. Wait longer between peeks as
  the run goes on. `list` shows every run the server holds.
- **A run you no longer need** (a wrong script, a stuck job): `stop` with
  its key. Its outputs are still sent with what it got to, and its answer
  says `stopped by its caller`. Don't just walk away from it, because it
  holds a machine until its limit.
- An answer nobody collected waits for its keep (`keep_minutes`, a few hours
  at most), then the key is free again.

## Secrets

A run is untrusted. Don't put tokens or passwords in `script` or `env`:
they are visible in the run and in its output. The server holds named
secrets. `"secrets": ["NAME"]` puts one into the run's environment, and
`"secret": "NAME"` on an input, output or log sends it as the Bearer header
of that transfer without letting it into the run. Use the second where you
can. Which names exist is the deployer's to tell you. Their values are masked
in everything that comes back.

## Pattern: a repository's tests, in isolation

1. Pack the code: the working tree as it is, without history or build
   output.

   ```bash
   tar --exclude=.git --exclude=node_modules --exclude=.venv -cf /tmp/repo.tar -C <repo> .
   # committed state only: git -C <repo> archive --format=tar -o /tmp/repo.tar HEAD
   curl -fsS -T /tmp/repo.tar "<outside>myproj/<attempt>/repo.tar"
   ```

2. Run it. Install with the network, test, write the report to a file, and
   exit with the tests' code:

   ```json
   {
     "key": "myproj-tests-<attempt>",
     "image": "python",
     "network": "internet",
     "env": {"HOME": "/tmp/home"},
     "limit_minutes": 20,
     "inputs": [{"url": "<inside>myproj/<attempt>/repo.tar", "to": "repo.tar"}],
     "outputs": [{"from": "out", "url": "<inside>myproj/<attempt>/out/"}],
     "script": "mkdir -p $HOME src out && tar -xf repo.tar -C src && cd src\npip install -q -r requirements.txt pytest\npython -m pytest -q --junitxml=../out/report.xml 2>&1 | tee ../out/report.txt\nexit ${PIPESTATUS[0]}"
   }
   ```

3. Read the answer. The first line tells you passed or failed, and the
   output tail shows the end. For the whole report, run
   `curl -fsS "<outside>myproj/<attempt>/out/report.txt"`.

Other stacks follow the same shape: `go test ./...` in a Go image,
`npm ci && npm test` in a Node one. Write down how the project is tested
in its `AGENTS.md`, so the next run doesn't have to work it out again.

## Pattern: one run per phase

When the tests must not reach the internet, or a step is slow and worth
keeping:

1. **prepare**, `network: internet`: install dependencies into `/work`
   (`pip install --target deps …`, `npm ci`, `go mod download` with
   `GOMODCACHE=/work/gomod`), pack them, send the pack out.
2. **test**, network `none` (the default): the code and that pack as
   inputs, the report as output.

Each phase has its own key. A later attempt can reuse the prepared pack
until the dependencies change.
