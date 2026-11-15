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
    AGENT_MINUTES=${AGENT_MINUTES:-40} agent "Do the task below in this folder. You may write only under $WRITE/; everything else is read-only. When you are done, $WRITE/check.sh must pass. Do not commit.

${BRIEF:?}" || agent_rc=$?
    sleep 1; kill "$spid" 2>/dev/null
    ;;
esac
t1=$(date +%s)
git add -A
echo "EVAL diff:"; git diff --cached --stat | tail -15
echo "EVAL stat $(git diff --cached --shortstat)"
[ -s /tmp/agent.jsonl ] && node -e '
let r = null;
for (const l of require("fs").readFileSync("/tmp/agent.jsonl", "utf8").split("\n")) { try { const e = JSON.parse(l); if (e.type === "result") r = e; } catch {} }
if (r) console.log("EVAL usage " + JSON.stringify({ turns: r.num_turns, s: Math.round(r.duration_ms / 1000), subtype: r.subtype, in: r.usage.input_tokens, cache_read: r.usage.cache_read_input_tokens, out: r.usage.output_tokens }));'

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
echo "EVAL mode=$MODE model=${ANTHROPIC_MODEL:-none} agent_rc=$agent_rc agent_s=$((t1 - t0)) peeked=$peeked self=$self hidden=$hidden check=$check"
