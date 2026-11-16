# evals

Past changes to units, done again by an agent and judged by the tests the real
change came with.

A case is a commit: the agent gets the module as it was before it (no git
history), a brief written from the commit's message and contract, and the Go
names the hidden tests will call. After it ends, the commit's own test files
are put over its work and run. Three results per run:

- **own gate** (`self`) — the module's `check.sh` on the agent's work, its own tests included: what a stage gate would have said;
- **hidden** — the reference tests: whether it did what the change did;
- **false green** — own gate PASS, hidden FAIL: the work a gate would have let through.

Each run is one [job_runner](../../job_runner/) run (`run_case.sh`), so the
agent never runs on the caller's machine. `eval.py check <case>` proves a case
first, without a model: on the old code the hidden tests fail, with the
reference change they pass. Where the old code does not even build the hidden
tests, `check` prints each build error with its line: every name there must be
in the brief, or the agent is judged on a guess.

```bash
export JOB_RUNNER_URL=… JOB_RUNNER_TOKEN=… LLM_BASE_URL=…   # the model's key is job_runner's secret LLM_KEY
python3 eval.py check jr-dirout
python3 eval.py run jr-dirout speak-deadline --model <group> -n 2 -v
python3 eval.py table
```

Results go to `_ignored/evals/results.jsonl` at the repository root.

`--harness mini` runs the same brief in mini-swe-agent (MIT, bash only)
instead of Claude Code, the model through the gateway's OpenAI door; its
trajectory is turned into the same STEP lines and usage after it ends, so
`peek` shows nothing while it works.

`--tests-by M` has model M write the acceptance tests from the brief alone
before the agent starts (the TDFlow shape: tests by another author, before the
code). They are committed and frozen: the agent is told to make them pass and
not to touch them, and they are put back as written before its own gate. The
hidden tests are judged without them. `frozen_base` / `frozen_oracle` say
whether they fail on the old code and pass on the reference change: a frozen
test the reference fails asked the agent for the wrong thing.

The own gate (`self`) fails on an empty change, as bot's `ws gate` does.

Not covered: the agent has the internet (for its model and Go modules), so it
could fetch the public reference itself; a brief that names Go symbols tells
the agent part of the design.
