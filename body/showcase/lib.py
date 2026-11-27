"""What the showcase's scripts have in common: a Redis stream read over RESP,
an MCP tool called over streamable HTTP (stateless, JSON), a body's GET /body,
and checks that stop the demo on the first failure."""

import json
import re
import socket
import time
import urllib.request as u


def check(ok, what):
    if not ok:
        raise SystemExit("FAIL: " + what)
    print("ok:", what, flush=True)


def get(url):
    return json.load(u.urlopen(url, timeout=10))


def wait(what, fn, seconds=120):
    """fn() until it gives something truthy; that, or a failure after seconds."""
    deadline = time.monotonic() + seconds
    while True:
        try:
            got = fn()
            if got:
                return got
        except Exception:
            pass
        if time.monotonic() > deadline:
            raise SystemExit("FAIL: timed out waiting for " + what)
        time.sleep(0.5)


def tool(mcp_url, name, args, timeout=120):
    """One tools/call; the structured answer, or SystemExit on a tool error."""
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                       "params": {"name": name, "arguments": args}}).encode()
    req = u.Request(mcp_url, data=body, headers={
        "Content-Type": "application/json", "Accept": "application/json, text/event-stream"})
    res = json.load(u.urlopen(req, timeout=timeout))["result"]
    if res.get("isError"):
        raise SystemExit(f"FAIL: {name}: {res['content'][0]['text']}")
    return res["structuredContent"]


class Redis:
    def __init__(self, host="redis", port=6379):
        self.f = socket.create_connection((host, port)).makefile("rwb")

    def command(self, *args):
        out = b"*%d\r\n" % len(args)
        for a in args:
            a = a.encode() if isinstance(a, str) else a
            out += b"$%d\r\n%s\r\n" % (len(a), a)
        self.f.write(out)
        self.f.flush()
        return self._read()

    def _read(self):
        line = self.f.readline().rstrip(b"\r\n")
        kind, rest = line[:1], line[1:]
        if kind == b"-":
            raise SystemExit("redis: " + rest.decode())
        if kind == b"*":
            n = int(rest)
            return None if n < 0 else [self._read() for _ in range(n)]
        if kind == b"$":
            n = int(rest)
            return None if n < 0 else self.f.read(n + 2)[:-2].decode()
        return rest.decode()

    def entries(self, stream):
        """Every entry of a stream as a dict, data parsed."""
        out = []
        for _, fields in self.command("XRANGE", stream, "-", "+") or []:
            e = dict(zip(fields[::2], fields[1::2]))
            e["data"] = json.loads(e.get("data") or "null")
            out.append(e)
        return out


def sse(url, want, seconds=30):
    """Read server-sent events at url until want(events) is truthy; the events."""
    got, cur = [], {}
    deadline = time.monotonic() + seconds
    with u.urlopen(url, timeout=seconds) as res:
        check(res.headers.get("Content-Type") == "text/event-stream", "GET " + url + " is an event stream")
        for line in res:
            line = line.decode().rstrip("\n")
            if line.startswith("id: "):
                cur["id"] = line[4:]
            elif line.startswith("data: "):
                cur["event"] = json.loads(line[6:])
            elif line == "" and cur:
                got.append(cur)
                cur = {}
                if want(got):
                    return got
            if time.monotonic() > deadline:
                break
    raise SystemExit("FAIL: timed out reading " + url)


def words(s):
    return set(re.findall(r"[a-z']+", (s or "").lower()))


def heard(entries, said, at_least=0.6):
    """The recognition among entries whose words are most of what was said."""
    want = words(said)
    for e in entries:
        if e["ce_type"] != "speechrecog.recognition-complete":
            continue
        got = words(e["data"].get("text"))
        if want and len(want & got) / len(want) >= at_least:
            return e
    return None


def acts(entries, tool_name=None):
    return [e for e in entries if e["ce_type"] == "body.act-complete"
            and (tool_name is None or e["data"]["tool"] == tool_name)]
