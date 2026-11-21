#!/bin/sh
# The tool over MCP, stateless: its list, a picture drawn, a fast one, and a
# refusal. Each answer is shown with the picture's bytes cut short.
set -u
T=http://images:8080/mcp
step() { printf '\n== %s\n' "$1"; }
mcp() {
  curl -s "$T" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}"
}
short() { sed -E 's/"data":"([A-Za-z0-9+\/]{24})[A-Za-z0-9+\/=]*"/"data":"\1…"/g'; }

for _ in $(seq 30); do curl -sf http://images:8080/health >/dev/null && break; sleep 1; done

step "the tools it offers"
mcp tools/list '{}' | short; echo

step "draw: a red square"
red=$(mcp tools/call '{"name":"draw","arguments":{"prompt":"a red square on nothing"}}')
echo "$red" | short; echo
echo "$red" | grep -q '"type":"image"' && echo "$red" | grep -q '"mimeType":"image/png"' || { echo "FAIL: no picture"; exit 1; }

step "draw, fast: a blue one, asked of the fast model"
mcp tools/call '{"name":"draw","arguments":{"prompt":"a blue square","fast":true,"size":"256x256"}}' | short; echo

step "refused: the endpoint's allowance is spent, said as the tool's error"
spent=$(mcp tools/call '{"name":"draw","arguments":{"prompt":"anything, spent"}}')
echo "$spent"
echo "$spent" | grep -q '"isError":true' || { echo "FAIL: not an error"; exit 1; }
