#!/bin/sh
# The tool over MCP, stateless: its list, a public page read, the stack and
# the cloud's metadata refused, and a search (Brave's, when BRAVE_API_KEY is
# set; else said to be not configured, as the tool's error).
set -u
T=http://web:8080/mcp
step() { printf '\n== %s\n' "$1"; }
mcp() {
  curl -s "$T" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}"
}
short() { cut -c1-900; }

for _ in $(seq 30); do curl -sf http://web:8080/health >/dev/null && break; sleep 1; done

step "the tools it offers"
mcp tools/list '{}' | short; echo

step "fetch: a public page, as markdown"
page=$(mcp tools/call '{"name":"fetch","arguments":{"url":"https://example.com/","max_chars":600}}')
echo "$page" | short; echo
echo "$page" | grep -q '"isError":true' && { echo "FAIL: example.com not read"; exit 1; }
echo "$page" | grep -q 'Example Domain' || { echo "FAIL: not the page"; exit 1; }

step "fetch: the stack's own page (http://page:8000/), refused: from the fetcher it is no name"
in=$(mcp tools/call '{"name":"fetch","arguments":{"url":"http://page:8000/"}}')
echo "$in"
echo "$in" | grep -q '"isError":true' || { echo "FAIL: the stack was reached"; exit 1; }

step "fetch: the one neighbour the fetcher can name (web itself), refused by the address check"
self=$(mcp tools/call '{"name":"fetch","arguments":{"url":"http://web:8080/health"}}')
echo "$self"
echo "$self" | grep -q 'not a public address' || { echo "FAIL: not refused"; exit 1; }

step "fetch: the cloud's metadata, refused"
md=$(mcp tools/call '{"name":"fetch","arguments":{"url":"http://169.254.169.254/latest/meta-data/"}}')
echo "$md"
echo "$md" | grep -q 'not a public address' || { echo "FAIL: not refused"; exit 1; }

step "fetch: not http, refused"
mcp tools/call '{"name":"fetch","arguments":{"url":"file:///etc/passwd"}}'; echo

step "search"
found=$(mcp tools/call '{"name":"search","arguments":{"query":"model context protocol","count":3}}')
echo "$found" | short; echo
if [ -n "${BRAVE_API_KEY:-}" ]; then
  echo "$found" | grep -q '"isError":true' && { echo "FAIL: search failed"; exit 1; }
else
  echo "$found" | grep -q 'search is not configured' || { echo "FAIL: not said"; exit 1; }
  echo "(no BRAVE_API_KEY here: search says so; fetch works all the same)"
fi
