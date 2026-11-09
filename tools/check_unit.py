#!/usr/bin/env python3
"""Hold a unit's unit.yaml to its code, and the units to each other.

A manifest nobody checks drifts; this is what makes unit.yaml something a
reader outside the unit may trust. Per unit:

- unit: the field equals the unit's path in this repository;
- env: the variables the code reads (Go, Python) equal config's required +
  optional + secrets (REDIS_* come with `infra: [redis]`, through
  units/shared/redis);
- events: the type strings in the code under the unit's prefixes equal
  provides.events.types, and the stream name appears in the code. A prefix is
  the unit's own name or the first segment of a declared type: hearing's events
  are named for the MRCPv2 resource it hosts (speechrecog.start-of-input);
- mcp: the tool names the code registers include provides.mcp.tools;
- consumes: a unit named in consumes.units has a manifest here, and what is
  used from it (tools, event types) is in that manifest's provides. A name
  that is no unit of this repository (a router, a database) is noted, not
  checked.

    python3 tools/check_unit.py                     # every unit
    python3 tools/check_unit.py senses/hearing      # one, by path
    python3 ../../tools/check_unit.py .             # from inside a unit
"""

import glob
import os
import re
import sys

import yaml

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
SHARED_REDIS_ENV = {"REDIS_ADDR", "REDIS_CONNECT_TIMEOUT"}
SKIP_DIRS = {"showcase", "node_modules", ".git", "__pycache__"}


def all_units():
    out = []
    for path in glob.glob(os.path.join(ROOT, "**", "unit.yaml"), recursive=True):
        rel = os.path.relpath(os.path.dirname(path), ROOT)
        if not SKIP_DIRS & set(rel.split(os.sep)):
            out.append(rel.replace(os.sep, "/"))
    return sorted(out)


def sources(unit):
    go, py = [], []
    for path in glob.glob(os.path.join(ROOT, unit, "**", "*"), recursive=True):
        name = os.path.basename(path)
        if name.endswith(".go") and not name.endswith("_test.go"):
            go.append(path)
        elif name.endswith(".py") and not name.startswith("test_"):
            py.append(path)
    text = []
    for path in go + py:
        with open(path, encoding="utf-8") as f:
            text.append(f.read())
    return "\n".join(text)


def load(unit):
    with open(os.path.join(ROOT, unit, "unit.yaml"), encoding="utf-8") as f:
        return yaml.safe_load(f)


def code_env(src):
    # Go: os.Getenv("X"), and helpers env("X", …), envInt, envOr, mustEnv, …
    names = set(re.findall(r'(?:Getenv|LookupEnv)\("([A-Z0-9_]+)"\)', src))
    names |= set(re.findall(r'\b\w*[eE]nv\w*\("([A-Z0-9_]+)"', src))
    # Python: os.environ["X"], os.environ.get("X"), os.getenv("X"), env.get("X")
    names |= set(re.findall(r'\b\w*[eE]nv\w*(?:\.get)?\(\s*["\']([A-Z0-9_]+)["\']', src))
    names |= set(re.findall(r'environ\[\s*["\']([A-Z0-9_]+)["\']', src))
    # Python: a name kept in a constant, ENV_TTL = "X", read as env.get(ENV_TTL)
    names |= set(re.findall(r'^\s*\w*ENV\w*\s*=\s*["\']([A-Z0-9_]+)["\']', src, re.M))
    return names


def check(unit, provider):
    m = load(unit)
    src = sources(unit)
    errs = []

    if m.get("unit") != unit:
        errs.append(f"unit: {m.get('unit')!r}, the path is {unit!r}")

    cfg = m.get("config") or {}
    declared = set()
    for part in ("required", "optional", "secrets"):
        declared |= set((cfg.get(part) or {}).keys())
    read = code_env(src)
    if "redis" in ((m.get("consumes") or {}).get("infra") or []):
        if '"github.com/jurasikasan/units/shared/redis"' not in src:
            errs.append("infra lists redis, but the code does not use units/shared/redis")
        declared |= SHARED_REDIS_ENV
    for name in sorted(read - declared):
        errs.append(f"env {name}: read by the code, not in config")
    for name in sorted(declared - read - SHARED_REDIS_ENV):
        errs.append(f"env {name}: in config, not read by the code")

    provides = m.get("provides") or {}
    ev = provides.get("events")
    prefixes = {unit.split("/")[-1]} | {t.split(".")[0] for t in ((ev or {}).get("types") or [])}
    in_code = set()
    for p in prefixes:
        in_code |= set(re.findall(r'"(%s(?:\.[a-z_-]+){1,2})"' % re.escape(p), src))
    if ev:
        if f'"{ev.get("stream")}"' not in src:
            errs.append(f"events: stream {ev.get('stream')!r} not in the code")
        if ev.get("source") and f'"{ev["source"]}"' not in src:
            errs.append(f"events: source {ev['source']!r} not in the code")
        types = set(ev.get("types") or [])
        for t in sorted(in_code - types):
            errs.append(f"event {t}: in the code, not in provides.events.types")
        for t in sorted(types - in_code):
            errs.append(f"event {t}: in provides.events.types, not in the code")
    elif in_code:
        errs.append(f"the code names events {sorted(in_code)} but provides no events")

    mcp = provides.get("mcp")
    if mcp:
        registered = set(re.findall(r'Name:\s*"([a-z_]+)"', src))
        for t in sorted(set(mcp.get("tools") or []) - registered):
            errs.append(f"mcp tool {t}: declared, not registered in the code")

    errs += check_consumes(unit, m, provider)
    return errs


def check_consumes(unit, m, provider):
    """What m uses of other units is in their manifests. provider(name) is the
    other unit's manifest, or None when the name is no unit it knows."""
    errs = []
    for use in ((m.get("consumes") or {}).get("units") or []):
        other = use.get("unit")
        om = provider(other)
        if om is None:
            print(f"  note: {unit} consumes {other}, which is no unit here")
            continue
        op = om.get("provides") or {}
        if use.get("via") == "mcp":
            have = set((op.get("mcp") or {}).get("tools") or [])
            for t in sorted(set(use.get("tools") or []) - have):
                errs.append(f"consumes {other} tool {t}: {other} does not provide it")
        if use.get("via") == "events":
            oev = op.get("events") or {}
            if use.get("stream") != oev.get("stream"):
                errs.append(f"consumes {other} stream {use.get('stream')!r}: {other} provides {oev.get('stream')!r}")
            for t in sorted(set(use.get("types") or []) - set(oev.get("types") or [])):
                errs.append(f"consumes {other} event {t}: {other} does not provide it")
        if use.get("via") == "http":
            have = set((op.get("http") or {}).get("calls") or [])
            if have:
                for c in sorted(set(use.get("calls") or []) - have):
                    errs.append(f"consumes {other} call {c!r}: {other} does not provide it")
    return errs


def local_provider(name):
    if name and os.path.isfile(os.path.join(ROOT, name, "unit.yaml")):
        return load(name)
    return None


def main(argv):
    targets = []
    for a in argv or all_units():
        rel = os.path.relpath(os.path.abspath(a), ROOT) if a.startswith(".") else a
        targets.append(rel.replace(os.sep, "/"))
    bad = 0
    for unit in targets:
        if not os.path.isfile(os.path.join(ROOT, unit, "unit.yaml")):
            print(f"{unit}: no unit.yaml")
            bad = 1
            continue
        errs = check(unit, local_provider)
        print(f"{unit}: {'ok' if not errs else f'{len(errs)} problem(s)'}")
        for e in errs:
            print(f"  {e}")
        bad |= bool(errs)
    return bad


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
