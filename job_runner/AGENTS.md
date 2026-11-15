# job_runner — setting it up, for an agent

You are an agent, and someone gave you this repository and asked you to use
job_runner. This file gets you from that to a working first call. How to use
it well afterwards is [SKILL.md](SKILL.md); what it promises is
[CONTRACT.md](CONTRACT.md).

job_runner runs a script in a fresh, isolated machine (a container under
gVisor, no network unless asked) and answers when it has ended. You reach it
as an MCP server over streamable HTTP with four tools: `run`, `peek`, `stop`, `list`.
You install nothing of ours into your project; you add a server to your MCP
client.

## 1. Which job_runner

Two ways to have one. Ask your human which, unless they already said.

- **Remote**: someone runs one for you. You need values from the human (below).
  Never guess a URL or make up a token.
- **Local**: you start one on this machine with Docker. Nothing to ask for
  except permission to run Docker.

Either way you end up with four values:

| Value | What it is | Local |
|---|---|---|
| `JOB_RUNNER_URL` | the MCP endpoint | `http://127.0.0.1:7480/` |
| `JOB_RUNNER_TOKEN` | the `Authorization: Bearer` token for it, if any | none |
| files, outside | the prefix where *you* PUT and GET files | `http://127.0.0.1:7481/v1/` |
| files, inside | the same files as job_runner reaches them; what goes into `url` in a run's `inputs` and `outputs` | `http://files:8000/v1/` |

Files pass through a file server. job_runner fetches a run's inputs from it
and sends the outputs back to it. You never pass bytes through the MCP call.
The two prefixes name the same file: what you PUT at
`<outside>demo/repo.tar` a run reads as `<inside>demo/repo.tar`.

### Remote

Ask the human for the MCP URL, the token, and the files prefix you upload to.
A common setup is a gate (nginx with an auth service) in front of job_runner
and a file store, with one token for both:

```
JOB_RUNNER_URL   https://<host>/jobs/
files, outside   https://<host>/store/v1/          (PUT/GET with the same Bearer token)
files, inside    http://artifact_store:8094/v1/    (the store's address behind the gate)
```

You don't need to be told the inside prefix. A `run` whose input `url` is not
under it fails at once with `not under <inside prefix>`, so one test call
finds it. The same goes for images: naming an unknown one answers with the
list (`not one of shell, python, …`).

Keep the token out of tracked files: put it in an environment variable
(`JOB_RUNNER_TOKEN`) or your client's user-level config.

### Local

Needs Docker on Linux, x86_64. The daemon container runs privileged, because
it is the layer under gVisor. Tell your human that before starting it.

```bash
mkdir -p ~/.job_runner && cd ~/.job_runner
curl -fsSLO https://raw.githubusercontent.com/jurasikasan/units/main/job_runner/local/compose.yaml
docker compose up -d --wait
```

The first start builds two images from the tagged source and takes a few
minutes. It listens on 127.0.0.1 only, with no token. The ports can be changed
with `JOB_RUNNER_PORT` and `FILES_PORT`. The images a run may name are
`shell` (bash, the default), `python`, `node` and `go`. Change them with
`JOB_RUNNER_IMAGES=name=reference,…` before `up`. Stop it with
`docker compose down`; add `-v` to drop the pulled images and files too.

Keep the compose file outside the project you are working in: it is a tool on
this machine, not part of that project.

## 2. Add the server to your client

Name it `job_runner`. The `run` tool answers only when the run has ended,
which can take an hour, so raise your client's tool timeout where it has one.
If a call still times out, the run goes on: see SKILL.md.

**Claude Code**

```bash
claude mcp add --transport http --scope user job_runner "$JOB_RUNNER_URL" \
  --header "Authorization: Bearer $JOB_RUNNER_TOKEN"     # leave out --header for local
```

The tools appear in the next session (`claude mcp list` shows the server
now). `MCP_TOOL_TIMEOUT` (milliseconds) is the tool timeout.

**Codex** (`~/.codex/config.toml`)

```toml
[mcp_servers.job_runner]
url = "http://127.0.0.1:7480/"
bearer_token_env_var = "JOB_RUNNER_TOKEN"   # remote only
tool_timeout_sec = 3600
```

**OpenCode** (`opencode.json`)

```json
{
  "mcp": {
    "job_runner": {
      "type": "remote",
      "url": "http://127.0.0.1:7480/",
      "headers": { "Authorization": "Bearer {env:JOB_RUNNER_TOKEN}" }
    }
  }
}
```

**Any client reading `.mcp.json`**

```json
{
  "mcpServers": {
    "job_runner": {
      "type": "http",
      "url": "http://127.0.0.1:7480/",
      "headers": { "Authorization": "Bearer ${JOB_RUNNER_TOKEN}" }
    }
  }
}
```

**No client support, or the tools are not loaded yet**: it is JSON-RPC over
HTTP POST. Initialize, keep the `Mcp-Session-Id` header from the reply, then
call tools. Answers come as JSON or as one SSE `data:` line.

```bash
H=(-H "Content-Type: application/json" -H "Accept: application/json, text/event-stream")
[ -n "${JOB_RUNNER_TOKEN:-}" ] && H+=(-H "Authorization: Bearer $JOB_RUNNER_TOKEN")
SID=$(curl -sS -D - -o /dev/null "${H[@]}" "$JOB_RUNNER_URL" -d \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}' \
  | tr -d '\r' | awk -F': ' 'tolower($1)=="mcp-session-id"{print $2}')
H+=(-H "Mcp-Session-Id: $SID")
curl -sS "${H[@]}" "$JOB_RUNNER_URL" -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'
curl -sS -m 3600 "${H[@]}" "$JOB_RUNNER_URL" -d \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run","arguments":{"key":"hello-1","script":"echo hello"}}}'
```

## 3. Prove it works

1. `run` with `{"key": "hello-<something unique>", "script": "echo hello"}`.
   The answer starts with `exit code 0 after …` and its output is `hello`.
2. `peek` with the same key: `ended`, with the same answer.
3. Files round trip. PUT a file outside, have a run read it and write
   another, then GET that one outside:

   ```bash
   echo ping > /tmp/ping.txt
   curl -fsS -T /tmp/ping.txt "<outside>hello/ping.txt"     # plus the Bearer header when remote
   ```

   `run` with `{"key": "files-<unique>", "script": "mkdir -p out && tr a-z A-Z < ping.txt > out/pong.txt",
   "inputs": [{"url": "<inside>hello/ping.txt", "to": "ping.txt"}],
   "outputs": [{"from": "out", "url": "<inside>hello/out/"}]}`. The answer lists
   `out/pong.txt -> PUT 201`, and `curl -fsS "<outside>hello/out/pong.txt"` prints
   `PING`.

Then tell your human it works, and which values you used, but not the token.

## 4. The skill

[SKILL.md](SKILL.md) is a skill in the Agent Skills format (a folder with a
`SKILL.md`). It says when to use job_runner and how. Put it where your client
reads skills, in a folder named after the skill, `job-runner`:

- Claude Code: `~/.claude/skills/job-runner/SKILL.md`
- other clients: their skills directory; where there is none, a line in the
  project's own `AGENTS.md` pointing at it

```bash
mkdir -p ~/.claude/skills/job-runner
curl -fsSL -o ~/.claude/skills/job-runner/SKILL.md \
  https://raw.githubusercontent.com/jurasikasan/units/main/job_runner/SKILL.md
```

Put your values (URL, both file prefixes) into a line of the project's
`AGENTS.md` or `CLAUDE.md`, never the token. The next session then doesn't
have to ask again.
