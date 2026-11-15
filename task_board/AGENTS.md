# task_board — setting it up, for an agent

You are an agent, and someone gave you this repository and asked you to use
the task board. This file gets you to a working first call. How to work the
board well is [SKILL.md](SKILL.md); what it promises is
[CONTRACT.md](CONTRACT.md).

The board is an MCP server over streamable HTTP (`list_tasks`, `get_task`,
`create_task`, `set_status`, `set_assignee`, `edit_task`,
`append_to_description`, `give_up`), plus an HTTP API under `/api/v1` for
what MCP leaves out (projects, dependencies).

## 1. Which board

A board is most useful when it is shared: people and agents see the same
tasks. So first ask your human whether there is one.

- **Remote**: someone runs one. Ask for the MCP URL, the token, the project
  key you work in, and the nick you sign as. Never guess a URL or make up a
  token. A common setup puts it behind a gate (nginx with an auth service):
  `https://<host>/…/mcp`, with the token sent as `Authorization: Bearer`.
  The board's HTTP API is usually behind the same gate.
- **Local**: a board on this machine, for you and your human only.

  ```bash
  mkdir -p ~/.task_board && cd ~/.task_board
  curl -fsSLO https://raw.githubusercontent.com/jurasikasan/units/main/task_board/local/compose.yaml
  docker compose up -d --wait
  ```

  Needs Docker. The MCP endpoint is `http://127.0.0.1:7482/mcp` and the HTTP
  API is `http://127.0.0.1:7482/api/v1/`, with no token, on 127.0.0.1 only
  (`TASK_BOARD_PORT` changes the port). Tasks are kept in a Docker volume:
  `docker compose down` keeps them, `down -v` drops them. A fresh board has
  no project, so make one (ask your human for the key, 2–10 capitals):

  ```bash
  curl -fsS -X POST http://127.0.0.1:7482/api/v1/projects \
    -H 'Content-Type: application/json' -d '{"key": "ABC", "name": "my project"}'
  ```

## 2. Add the server to your client

Name it `task_board`. The same shapes as for any streamable-HTTP MCP server:

```bash
# Claude Code; leave out --header for a local board
claude mcp add --transport http --scope local task_board "$TASK_BOARD_URL" \
  --header "Authorization: Bearer $TASK_BOARD_TOKEN"
```

```toml
# Codex, ~/.codex/config.toml
[mcp_servers.task_board]
url = "http://127.0.0.1:7482/mcp"
bearer_token_env_var = "TASK_BOARD_TOKEN"   # remote only
```

```json
{ "mcpServers": { "task_board": { "type": "http", "url": "http://127.0.0.1:7482/mcp",
  "headers": { "Authorization": "Bearer ${TASK_BOARD_TOKEN}" } } } }
```

`--scope local` keeps the token out of the project's files: a board belongs to
a project, but its token must not go into the repository. The tools appear in
the next session.

## 3. Prove it works

1. `list_tasks` with `{"project": "<KEY>"}` gives the project's tasks, or
   none.
2. On a board of your own, or with your human's go-ahead: `create_task` with
   `{"project": "<KEY>", "goal": "try the board", "acceptance_criteria":
   "this task exists and is CANCELLED", "actor": "<your nick>"}` → `<KEY>-1`,
   DRAFT. Then `set_status` it to `CANCELLED`, because it was never meant to
   be done.

Then tell your human which board, project and nick you use, but not the token.

## 4. The skill

[SKILL.md](SKILL.md) is in the Agent Skills format. Put it where your client
reads skills, in a folder named `task-board`. For Claude Code:

```bash
mkdir -p ~/.claude/skills/task-board
curl -fsSL -o ~/.claude/skills/task-board/SKILL.md \
  https://raw.githubusercontent.com/jurasikasan/units/main/task_board/SKILL.md
```

Write the board's URL, project key and your nick into the project's
`AGENTS.md` or `CLAUDE.md`, never the token. The next session then doesn't
have to ask again.
