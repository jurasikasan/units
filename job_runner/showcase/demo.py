"""CONTRACT.md section 7 against the showcase's job_runner, as an agent
would call it: MCP over streamable HTTP, stdlib only."""
import json
import socket
import sys
import threading
import time
import urllib.request

JR = "http://job_runner:8080/"
FILES = "http://files:8000"
TAG = str(int(time.time()))
failed = []


def post(body, sid=None):
    h = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    if sid:
        h["Mcp-Session-Id"] = sid
    req = urllib.request.Request(JR, data=json.dumps(body).encode(), headers=h)
    with urllib.request.urlopen(req, timeout=600) as r:
        sid = r.headers.get("Mcp-Session-Id", sid)
        raw = r.read().decode()
    msg = None
    for line in raw.splitlines():
        if line.startswith("data: "):
            msg = json.loads(line[6:])
        elif line.startswith("{"):
            msg = json.loads(line)
    return msg, sid


def tool(name, args):
    """One MCP session per call, as a stateless agent makes it."""
    _, sid = post({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
        "protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "demo", "version": "1"}}})
    post({"jsonrpc": "2.0", "method": "notifications/initialized"}, sid)
    msg, _ = post({"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": name, "arguments": args}}, sid)
    res = msg["result"]
    return res["content"][0]["text"], res.get("structuredContent"), res.get("isError", False)


def check(name, ok, shown=""):
    print(("  ✓ " if ok else "  ✗ ") + name)
    if not ok:
        failed.append(name)
        print("    " + shown.replace("\n", "\n    "))


def step(t):
    print("\n== " + t)


def run(key, script, **kw):
    return tool("run", {"key": f"{key}-{TAG}", "script": script, **kw})


def seen():
    with urllib.request.urlopen(FILES + "/_seen") as r:
        return json.load(r)


step("1. a script runs and answers")
t, sc, _ = run("hello", "echo hello")
print(t)
check("exit code 0, the output", t.startswith("exit code 0 after") and t.endswith("\nhello"), t)
check("the same as structured content", sc["exit_code"] == 0 and sc["output_tail"] == "hello", json.dumps(sc))

step("2. peek while it runs, the same key for the same run, list")
box = {}
th = threading.Thread(target=lambda: box.update(a=run("slow", "for i in 1 2 3 4 5 6; do echo step $i; sleep 2; done; echo done")))
th.start()
time.sleep(5)
t, sc, _ = tool("peek", {"key": f"slow-{TAG}", "lines": 2})
print(t)
check("peek: running, the last lines, at once", sc["state"] == "running" and "step" in sc["output_tail"], t)
t, _, _ = tool("list", {})
print(t)
check("list shows it running", f"slow-{TAG}  running" in t, t)
t2, _, _ = run("slow", "echo something else")
th.join()
check("the same key gets that run's answer", t2 == box["a"][0] and t2.endswith("done"), t2)
t, sc, _ = tool("peek", {"key": f"slow-{TAG}"})
check("peek: ended, with the answer", sc["state"] == "ended" and sc["answer"]["exit_code"] == 0, t)
t, sc, _ = tool("peek", {"key": "never-was"})
check("peek: unknown", sc["state"] == "unknown", t)

step("3. files in and out; the token stays outside")
t, sc, _ = run("files", "cat in/greeting.txt; env | grep -c showcase-token; mkdir out; tr a-z A-Z < in/greeting.txt > out/shout.txt",
               inputs=[{"url": FILES + "/v1/files/greeting.txt", "to": "in/greeting.txt", "secret": "FILES_TOKEN"}],
               outputs=[{"from": "out/shout.txt", "url": FILES + f"/v1/files/shout-{TAG}.txt", "secret": "FILES_TOKEN"},
                        {"from": "out/none.txt", "url": FILES + f"/v1/files/none-{TAG}.txt"}])
print(t)
check("the input was there", "hello from outside" in t, t)
check("the token is not in the run's environment", "\n0\n" in t + "\n", t)
check("the change report: +1", "Changes: +1 ~0 -0" in t and "+ out/shout.txt" in t, t)
check("an output sent, a missing one named", "out/shout.txt -> PUT 201" in t and "out/none.txt -> missing" in t, t)
with urllib.request.urlopen(FILES + f"/v1/files/shout-{TAG}.txt") as r:
    body = r.read().decode()
check("the output reached the file server", body == "HELLO FROM OUTSIDE\n", body)
auth = {s["path"]: s["auth"] for s in seen() if s["method"] == "PUT"}
check("the token went with the transfers, as a bearer", auth.get(f"/v1/files/shout-{TAG}.txt") == "Bearer showcase-token", json.dumps(auth))
t, _, err = run("elsewhere", "true", inputs=[{"url": "https://example.com/x", "to": "x", "secret": "FILES_TOKEN"}])
check("a URL outside TRANSFER_URLS is refused", err and "not under" in t, t)

step("4. networks: none by default, internet is public only")
files_ip = socket.gethostbyname("files")
probe = "wget -q -T 5 -O /dev/null https://example.com && echo public-ok || echo public-no; " \
        f"nc -w 3 {files_ip} 8000 </dev/null && echo private-ok || echo private-no"
t, _, _ = run("net-none", probe)
print(t)
check("none: nothing answers", "public-no" in t and "private-no" in t, t)
t, _, _ = run("net-internet", probe, network="internet")
print(t)
check("internet: public yes, private no", "public-ok" in t and "private-no" in t, t)
t, _, err = run("net-host", "true", network="host")
check("a network the server lacks is refused", err, t)

step("5. gVisor's kernel; limits end the run, not the box")
t, _, _ = run("kernel", "dmesg 2>/dev/null | head -1; uname -r; id -u; grep -E 'CapEff|NoNewPrivs' /proc/self/status")
print(t)
check("gVisor, uid 1000, no capabilities, no_new_privs",
      "gVisor" in t and "\n1000\n" in t and "0000000000000000" in t and "NoNewPrivs:\t1" in t, t)
t, _, _ = run("forks", "for i in $(seq 400); do sleep 30 & done; echo all-started")
print(t)
check("a fork loop past RUN_PIDS ends the run", "all-started" not in t and not t.startswith("exit code 0"), t)
t, _, _ = run("memory", "x=$(head -c 400000000 /dev/zero | tr '\\0' a); echo ${#x}")
print(t)
check("an allocation past RUN_MEMORY ends the run", t.startswith("exit code 137"), t)

step("6. a secret is masked; the log goes out however the run ended")
t, _, _ = run("secret", 'echo "token: $FILES_TOKEN"; exit 3', secrets=["FILES_TOKEN"],
              log={"url": FILES + f"/v1/logs/secret-{TAG}.log"})
print(t)
check("masked in the answer", "token: ***" in t and "showcase-token" not in t, t)
with urllib.request.urlopen(FILES + f"/v1/logs/secret-{TAG}.log") as r:
    body = r.read().decode()
check("the log of a failed run, masked, with its answer", "token: ***" in body and "exit code 3 after" in body
      and "showcase-token" not in body, body)

step("7. a limit stops the run (a minute)")
t, sc, _ = run("limit", "sleep 600", limit_minutes=1)
print(t.splitlines()[0])
check("stopped by its limit", t.startswith("stopped by its limit") and sc["exit_code"] is None, t)

print("\nFAIL: " + ", ".join(failed) if failed else "\nOK")
sys.exit(1 if failed else 0)
