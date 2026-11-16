#!/bin/bash
# One eval case in one job_runner run (image with Go, git, node, Claude Code and
# the `agent` helper). MODE: control (nothing changed: the hidden tests must
# fail), oracle (the reference change: they must pass), agent (a model does
# the brief). The agent sees a cut at BASE with no history; the reference
# commit is fetched only after it has ended.
#
# Prints: STEP lines while the agent works (for peek), then
#   EVAL usage {...}
#   EVAL stat <files changed, insertions, deletions>
#   EVAL mode=… model=… agent_rc=… agent_s=… self=… hidden=… check=…
# self = the module's check.sh on the agent's own work; hidden = the reference
# tests over it; check = check.sh with them.
set -u
: "${REPO:?}" "${BASE:?}" "${REF:?}" "${CUT:?}" "${WRITE:?}" "${HIDDEN:?}" "${TESTS:?}" "${MODE:?}"
export HOME=/tmp/home; mkdir -p "$HOME"
cd /tmp
clone() { for _ in 1 2 3 4 5; do git clone -q "$REPO" "$1" && return 0; sleep 5; done; return 1; }
clone src || { echo "EVAL clone failed"; exit 2; }
mkdir ws
# shellcheck disable=SC2086
git -C src archive "$BASE" $CUT | tar -x -C ws
rm -rf src
cd ws
git init -q && git add -A && git -c user.email=e@e -c user.name=eval commit -qm base

steps() {  # one line per tool call of the agent's stream, to stdout
  tail -n +1 -F /tmp/agent.jsonl 2>/dev/null | node -e '
const rl = require("readline").createInterface({ input: process.stdin });
let n = 0;
rl.on("line", (l) => {
  let e; try { e = JSON.parse(l); } catch { return; }
  for (const c of (e.message && e.message.content) || []) {
    if (c.type === "tool_use") {
      const i = c.input || {};
      const what = i.command || i.file_path || i.pattern || i.path || "";
      console.log(`STEP ${++n} ${c.name} ${String(what).replace(/\s+/g, " ").slice(0, 120)}`);
    }
  }
});'
}

# Tools for the agent, installed before its clock starts.
if [ "${TOOLS:-}" = gopls ]; then
  GOBIN=/tmp/bin go install golang.org/x/tools/gopls@latest > /tmp/gopls.install 2>&1 \
    || { echo "EVAL gopls install failed"; tail -5 /tmp/gopls.install; }
fi

# One headless Claude Code call: claude_call MODEL MINUTES TURNS TOOLS OUT PROMPT.
# A claude-* model runs on the subscription token, any other on a LiteLLM
# group with the agent helper's settings.
claude_call() {
  local model=$1 minutes=$2 turns=$3 tools=$4 out=$5 prompt=$6
  if [ "${model#claude}" != "$model" ]; then
    env -u ANTHROPIC_BASE_URL CLAUDE_CODE_OAUTH_TOKEN="${CLAUDE_TOKEN:-}" DISABLE_AUTOUPDATER=1 PAGER=cat GIT_PAGER=cat \
      timeout -s INT -k 30s "${minutes}m" claude -p "$prompt" --model "$model" --max-turns "$turns" \
      --allowedTools "$tools" --permission-mode acceptEdits --output-format stream-json --verbose > "$out" 2> "$out.err"
  else
    ANTHROPIC_API_KEY=${LLM_KEY:-placeholder} ANTHROPIC_DEFAULT_SONNET_MODEL=$model \
      ANTHROPIC_DEFAULT_HAIKU_MODEL=$model ANTHROPIC_DEFAULT_OPUS_MODEL=$model \
      CLAUDE_CODE_DISABLE_1M_CONTEXT=1 CLAUDE_CODE_MAX_CONTEXT_TOKENS=90000 \
      DISABLE_AUTOUPDATER=1 PAGER=cat GIT_PAGER=cat \
      timeout -s INT -k 30s "${minutes}m" claude -p "$prompt" --bare --model "$model" --max-turns "$turns" \
      --allowedTools "$tools" --permission-mode acceptEdits --output-format stream-json --verbose > "$out" 2> "$out.err"
  fi
}

# A planner model rewrites the brief into PLAN_TEMPLATE before the agent
# starts; the agent then gets the plan instead of the brief. Its clock and
# turns are its own.
plan_s=0
if [ "$MODE" = agent ] && [ -n "${PLANNER:-}" ]; then
  p0=$(date +%s)
  claude_call "$PLANNER" 15 60 Read,Glob,Grep,Bash /tmp/plan.jsonl "${PLAN_TEMPLATE:?}${BRIEF:?}" || echo "EVAL planner rc=$?"
  plan_s=$(( $(date +%s) - p0 ))
  node -e '
let r = ""; for (const l of require("fs").readFileSync("/tmp/plan.jsonl", "utf8").split("\n")) { try { const e = JSON.parse(l); if (e.type === "result") r = e.result || ""; } catch {} }
require("fs").writeFileSync("/tmp/plan.md", r);' 
  git checkout -q -- . && git clean -qfd  # a planner writes nothing
  echo "EVAL plan begin"; cat /tmp/plan.md; echo; echo "EVAL plan end"
  [ -s /tmp/plan.md ] && BRIEF=$(cat /tmp/plan.md)
fi

agent_rc=-
t0=$(date +%s)
case "$MODE" in
  control) ;;
  oracle)
    clone /tmp/ref
    git -C /tmp/ref diff "$BASE" "$REF" -- "$WRITE" | git apply --exclude='*_test.go' && agent_rc=0 ;;
  agent)
    : > /tmp/agent.jsonl
    steps & spid=$!
    agent_rc=0
    prompt="Do the task below in this folder. You may write only under $WRITE/; everything else is read-only. When you are done, $WRITE/check.sh must pass. Do not commit.

${BRIEF:?}"
    if [ -n "${CLAUDE_TOKEN:-}" ]; then
      # Claude itself, on a subscription: the agent helper's --bare reads only
      # an API key, so the same call without it, the same tools and limits.
      env -u ANTHROPIC_BASE_URL CLAUDE_CODE_OAUTH_TOKEN="$CLAUDE_TOKEN" DISABLE_AUTOUPDATER=1 PAGER=cat GIT_PAGER=cat \
        timeout -s INT -k 30s "${AGENT_MINUTES:-40}m" claude -p "$prompt" --model "$ANTHROPIC_MODEL" \
        --max-turns 150 --allowedTools Read,Edit,Write,Bash,Glob,Grep --permission-mode acceptEdits \
        --output-format stream-json --verbose > /tmp/agent.jsonl 2> /tmp/agent.err || agent_rc=$?
      tail -3 /tmp/agent.err
    elif [ "${TOOLS:-}" = gopls ]; then
      # The same model and flags as the agent helper, plus Go's language
      # server as MCP tools (gopls mcp): symbols, references, diagnostics.
      printf '{"mcpServers":{"gopls":{"command":"sh","args":["-c","cd /tmp/ws/%s && exec /tmp/bin/gopls mcp"]}}}' "$WRITE" > /tmp/mcp.json
      ANTHROPIC_API_KEY=${LLM_KEY:-placeholder} ANTHROPIC_DEFAULT_SONNET_MODEL=$ANTHROPIC_MODEL \
        ANTHROPIC_DEFAULT_HAIKU_MODEL=$ANTHROPIC_MODEL ANTHROPIC_DEFAULT_OPUS_MODEL=$ANTHROPIC_MODEL \
        CLAUDE_CODE_DISABLE_1M_CONTEXT=1 CLAUDE_CODE_MAX_CONTEXT_TOKENS=90000 \
        DISABLE_AUTOUPDATER=1 PAGER=cat GIT_PAGER=cat \
        timeout -s INT -k 30s "${AGENT_MINUTES:-40}m" claude -p "$prompt" --bare --model "$ANTHROPIC_MODEL" \
        --mcp-config /tmp/mcp.json --max-turns 150 \
        --allowedTools Read,Edit,Write,Bash,Glob,Grep,mcp__gopls --permission-mode acceptEdits \
        --output-format stream-json --verbose > /tmp/agent.jsonl 2> /tmp/agent.err || agent_rc=$?
      tail -3 /tmp/agent.err
      echo "EVAL gopls calls: $(grep -o '"name":"mcp__gopls__[a-z_]*"' /tmp/agent.jsonl | sort | uniq -c | tr '\n' ' ')"
    else
      AGENT_MINUTES=${AGENT_MINUTES:-40} agent "$prompt" || agent_rc=$?
    fi
    sleep 1; kill "$spid" 2>/dev/null
    ;;
esac
t1=$(date +%s)
git add -A
echo "EVAL diff:"; git diff --cached --stat | tail -15
echo "EVAL stat $(git diff --cached --shortstat)"
[ -s /tmp/agent.jsonl ] && node -e '
// A session may end more than once (a model that schedules a wakeup gets one
// more short turn): every result event counts.
let r = null;
for (const l of require("fs").readFileSync("/tmp/agent.jsonl", "utf8").split("\n")) {
  let e; try { e = JSON.parse(l); } catch { continue; }
  if (e.type !== "result") continue;
  const u = e.usage || {};
  r = r || { turns: 0, s: 0, in: 0, cache_read: 0, out: 0, results: 0 };
  r.turns += e.num_turns || 0; r.s += Math.round((e.duration_ms || 0) / 1000); r.subtype = e.subtype; r.results++;
  r.in += u.input_tokens || 0; r.cache_read += u.cache_read_input_tokens || 0; r.out += u.output_tokens || 0;
}
if (r) console.log("EVAL usage " + JSON.stringify(r));'

# Did the agent go for the reference itself? Resourceful, but not the same result.
peeked=-
[ -s /tmp/agent.jsonl ] && peeked=no && grep -qiE '"(command|url)":"[^"]*(github\.com[/:][^"]*units|git (clone|fetch|ls-remote)|proxy\.golang\.org[^"]*units)' /tmp/agent.jsonl && peeked=yes

self=-
if [ "$MODE" = agent ]; then
  self=FAIL
  ( cd "$WRITE" && bash check.sh ) > /tmp/self.log 2>&1 && self=PASS
  tail -8 /tmp/self.log
fi

# The hidden tests: the reference commit's test files, over whatever is here now.
[ -d /tmp/ref ] || clone /tmp/ref
for f in $HIDDEN; do mkdir -p "$(dirname "$f")"; git -C /tmp/ref show "$REF:$f" > "$f"; done

hidden=FAIL
# shellcheck disable=SC2086
( cd "$WRITE" && go test -count=1 $TESTS ) > /tmp/hidden.log 2>&1 && hidden=PASS
tail -25 /tmp/hidden.log
# On the old code the hidden tests should fail to build only on names the brief
# gives: each such error is printed so `eval.py check` can hold it to the brief.
if [ "$MODE" = control ]; then
  # shellcheck disable=SC2086
  ( cd "$WRITE" && go test -count=1 -gcflags=-e -run '^$' $(echo $TESTS | sed 's/ -run .*//') ) 2>&1 | grep -E '^[^#].*_test\.go:[0-9]+:[0-9]+: ' | sort -u | while IFS= read -r l; do
    f=${l%%:*}; n=$(echo "$l" | cut -d: -f2)
    echo "EVAL builderr $l || $(sed -n "${n}p" "$WRITE/$f" | sed 's/^[[:space:]]*//')"
  done
fi
check=FAIL
( cd "$WRITE" && bash check.sh ) > /tmp/check.log 2>&1 && check=PASS
tail -10 /tmp/check.log
echo "EVAL mode=$MODE model=${ANTHROPIC_MODEL:-none} tools=${TOOLS:-none} planner=${PLANNER:-none} plan_s=$plan_s agent_rc=$agent_rc agent_s=$((t1 - t0)) peeked=$peeked self=$self hidden=$hidden check=$check"
