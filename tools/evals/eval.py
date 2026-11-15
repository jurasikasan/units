#!/usr/bin/env python3
"""Run eval cases through job_runner and keep the results.

  eval.py check <case>...                 control + oracle: the case itself is sound
  eval.py run <case>... --model M [-n N]  a model does each case N times
  eval.py table                           what the results say

A case is cases/<id>/case.env (REPO BASE REF CUT WRITE HIDDEN TESTS) and
cases/<id>/brief.md. Each run is one job_runner run of run_case.sh; this script
only starts it, waits with peek and records the EVAL lines it prints.

Env: JOB_RUNNER_URL, JOB_RUNNER_TOKEN; for agent runs LLM_BASE_URL (the
Anthropic-style door the agent's model is behind) — the key is job_runner's
secret LLM_KEY, never here. Results: ../../_ignored/evals/results.jsonl.
"""
import argparse
import concurrent.futures as cf
import datetime as dt
import json
import os
import re
import sys
import threading
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
RESULTS = os.path.join(HERE, "..", "..", "_ignored", "evals", "results.jsonl")
_lock = threading.Lock()


class MCP:
    def __init__(self, url, token):
        self.url, self.token, self.sid, self.n = url, token, None, 0
        self._call("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                  "clientInfo": {"name": "evals", "version": "0"}})
        self._post({"jsonrpc": "2.0", "method": "notifications/initialized"}, 30)

    def _post(self, msg, timeout):
        h = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
        if self.token:
            h["Authorization"] = "Bearer " + self.token
        if self.sid:
            h["Mcp-Session-Id"] = self.sid
        req = urllib.request.Request(self.url, json.dumps(msg).encode(), h)
        with urllib.request.urlopen(req, timeout=timeout) as r:
            self.sid = r.headers.get("Mcp-Session-Id") or self.sid
            body = r.read().decode()
        if not body.strip():
            return None
        if body.lstrip().startswith("{"):
            return json.loads(body)
        for line in body.splitlines():  # SSE: the answer is a data: line
            if line.startswith("data:"):
                d = json.loads(line[5:])
                if "id" in d:
                    return d
        return None

    def _call(self, method, params, timeout=60):
        self.n += 1
        r = self._post({"jsonrpc": "2.0", "id": self.n, "method": method, "params": params}, timeout)
        if r and "error" in r:
            raise RuntimeError(r["error"])
        return r["result"] if r else None

    def tool(self, name, args, timeout=60):
        res = self._call("tools/call", {"name": name, "arguments": args}, timeout)
        if res.get("isError"):
            raise RuntimeError(res["content"][0]["text"])
        return res.get("structuredContent") or json.loads(res["content"][0]["text"])


def load_case(cid):
    d = os.path.join(HERE, "cases", cid)
    env = {}
    for line in open(os.path.join(d, "case.env")):
        line = line.strip()
        if line and not line.startswith("#"):
            k, v = line.split("=", 1)
            env[k] = v
    env["BRIEF"] = open(os.path.join(d, "brief.md")).read()
    return env


def run_one(cid, mode, model, i, args):
    env = load_case(cid)
    env["MODE"] = mode
    secrets = []
    if mode == "agent":
        env.update(ANTHROPIC_BASE_URL=os.environ["LLM_BASE_URL"], ANTHROPIC_MODEL=model,
                   AGENT_MINUTES=str(args.agent_minutes), TASK="")
        secrets = ["LLM_KEY"]
    stamp = dt.datetime.now().strftime("%m%d%H%M%S")
    key = f"eval-{cid}-{mode}-{model or 'none'}-{stamp}-{i}"
    script = open(os.path.join(HERE, "run_case.sh")).read()
    mcp = MCP(os.environ["JOB_RUNNER_URL"], os.environ.get("JOB_RUNNER_TOKEN"))
    call = dict(key=key, script=script, image="agent", network="internet", env=env,
                secrets=secrets, limit_minutes=args.agent_minutes + 30)
    # run answers when the run ends; a proxy may cut the call first. The run
    # goes on either way, and peek with the same key gets its answer.
    threading.Thread(target=lambda: _quiet(mcp.tool, "run", call, 7200), daemon=True).start()
    say(f"{key}: started")
    seen = 0
    while True:
        time.sleep(30)
        try:
            p = MCP(os.environ["JOB_RUNNER_URL"], os.environ.get("JOB_RUNNER_TOKEN")).tool(
                "peek", {"key": key, "lines": 2000})
        except Exception as e:  # noqa: BLE001 — a failed peek is retried
            say(f"{key}: peek failed: {e}")
            continue
        out = (p.get("answer") or {}).get("output_tail") or p.get("output_tail") or ""
        steps = [l for l in out.splitlines() if l.startswith("STEP ")]
        if args.verbose:
            for l in steps[seen:]:
                say(f"{key}: {l}")
        seen = len(steps)
        if p.get("state") != "running":
            break
    a = p.get("answer") or {}
    rec = {"at": dt.datetime.now().isoformat(timespec="seconds"), "case": cid, "mode": mode,
           "model": model, "key": key, "exit": a.get("exit_code"),
           "stopped_by_limit": a.get("stopped_by_limit"), "duration_ms": a.get("duration_ms")}
    tail = a.get("output_tail", "")
    m = re.findall(r"^EVAL mode=.*$", tail, re.M)
    if m:
        rec.update(dict(kv.split("=", 1) for kv in m[-1].split()[1:]))
    u = re.findall(r"^EVAL usage (.*)$", tail, re.M)
    if u:
        rec["usage"] = json.loads(u[-1])
    be = re.findall(r"^EVAL builderr (.*)$", tail, re.M)
    if be:
        rec["builderr"] = be
    st = re.findall(r"^EVAL stat (.*)$", tail, re.M)
    if st:
        rec["stat"] = st[-1]
    os.makedirs(os.path.dirname(RESULTS), exist_ok=True)
    with _lock, open(RESULTS, "a") as f:
        f.write(json.dumps(rec) + "\n")
    for b in rec.get("builderr", []):  # every one must be answered by the brief
        say(f"{key}: build error on the old code: {b}")
    say(f"{key}: {' '.join(f'{k}={rec.get(k)}' for k in ('self', 'hidden', 'check', 'peeked', 'agent_rc', 'agent_s'))}")
    return rec


def _quiet(f, *a):
    try:
        f(*a)
    except Exception:  # noqa: BLE001 — the answer is taken by peek
        pass


def say(s):
    with _lock:
        print(s, flush=True)


def table():
    rows = [json.loads(l) for l in open(RESULTS)] if os.path.exists(RESULTS) else []
    agg = {}
    for r in rows:
        if r["mode"] != "agent" or r.get("void"):  # void: the case was wrong, not the run
            continue
        a = agg.setdefault((r["case"], r["model"]), {"n": 0, "hidden": 0, "self": 0, "false_green": 0, "s": 0, "turns": 0})
        a["n"] += 1
        a["hidden"] += r.get("hidden") == "PASS"
        a["self"] += r.get("self") == "PASS"
        a["false_green"] += r.get("self") == "PASS" and r.get("hidden") != "PASS"
        if "peeked" in r:  # runs before the detector have no answer
            a["peeked"] = a.get("peeked", 0) + (r["peeked"] == "yes")
        a["s"] += int(r.get("agent_s") or 0)
        a["turns"] += (r.get("usage") or {}).get("turns") or 0
    print(f"{'case':<14} {'model':<10} {'n':>2} {'hidden':>6} {'own gate':>8} {'false green':>11} {'avg s':>6} {'avg turns':>9} {'peeked':>6}")
    for (c, m), a in sorted(agg.items()):
        print(f"{c:<14} {m:<10} {a['n']:>2} {a['hidden']:>6} {a['self']:>8} {a['false_green']:>11} {a['s'] // a['n']:>6} {a['turns'] // a['n']:>9} {a.get('peeked', '-'):>6}")
    for r in rows:
        if r["mode"] != "agent":
            print(f"check {r['case']:<14} {r['mode']:<8} hidden={r.get('hidden')} check={r.get('check')}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("cmd", choices=["check", "run", "table"])
    ap.add_argument("cases", nargs="*")
    ap.add_argument("--model")
    ap.add_argument("-n", type=int, default=1)
    ap.add_argument("--parallel", type=int, default=2)
    ap.add_argument("--agent-minutes", type=int, default=40)
    ap.add_argument("-v", "--verbose", action="store_true")
    args = ap.parse_args()
    if args.cmd == "table":
        return table()
    jobs = []
    for c in args.cases:
        if args.cmd == "check":
            jobs += [(c, "control", None, 0), (c, "oracle", None, 0)]
        else:
            if not args.model:
                sys.exit("run needs --model")
            jobs += [(c, "agent", args.model, i) for i in range(args.n)]
    with cf.ThreadPoolExecutor(args.parallel) as ex:
        list(ex.map(lambda j: run_one(*j, args), jobs))


if __name__ == "__main__":
    main()
