#!/usr/bin/env python3
"""Every local module the service imports is copied into its image.

    python3 ../scripts/check_dockerfile_copies.py .     # from a module's check.sh

A Python service's gate runs its tests from the source tree, where every file
is next to every other; the image holds only what the Dockerfile's COPY lines
name. So a module split out of server.py passes the gate and kills the
container on its first import — loader.py in tts_piper, voices.py in
tts_silero, both caught only on review.

What it reads, without building anything: the entry module from CMD
(`uvicorn server:app` → server.py, `python x.py` → x.py; server.py when CMD
says neither), every import in it and in the local modules it reaches —
anywhere in the file, a lazy import inside a function included — and the
sources of the COPY lines. A name that resolves to a .py file or a package
directory beside the Dockerfile must be among those sources; `COPY . .` or a
matching glob covers it. Standard library only: the gate's image has python3
and nothing else.
"""
import ast
import fnmatch
import json
import os
import re
import shlex
import sys


def dockerfile_lines(path):
    """Instructions with their continuation lines joined, comments dropped."""
    out, cur = [], ""
    with open(path, encoding="utf-8") as f:
        for raw in f:
            line = raw.rstrip("\n")
            if not cur and line.lstrip().startswith("#"):
                continue
            if line.endswith("\\"):
                cur += line[:-1] + " "
                continue
            cur += line
            if cur.strip():
                out.append(cur.strip())
            cur = ""
    if cur.strip():
        out.append(cur.strip())
    return out


def args_of(rest):
    """An instruction's arguments, in either the JSON or the shell form."""
    rest = rest.strip()
    if rest.startswith("["):
        try:
            return json.loads(rest)
        except json.JSONDecodeError:
            pass
    return shlex.split(rest)


def copied_sources(lines):
    sources = []
    for line in lines:
        word, _, rest = line.partition(" ")
        if word.upper() not in ("COPY", "ADD"):
            continue
        args = args_of(rest)
        # --from= copies out of another stage or image, not out of this folder.
        if any(a.startswith("--from") for a in args):
            continue
        args = [a for a in args if not a.startswith("--")]
        sources += [os.path.normpath(a) for a in args[:-1]]  # the last one is the destination
    return sources


def entry_module(lines):
    for line in reversed(lines):
        word, _, rest = line.partition(" ")
        if word.upper() not in ("CMD", "ENTRYPOINT"):
            continue
        for a in args_of(rest):
            m = re.fullmatch(r"([A-Za-z_][\w.]*):\w+", a)  # uvicorn module:app
            if m:
                return m.group(1).replace(".", "/") + ".py"
            if a.endswith(".py"):
                return a
    return "server.py"


def local_target(root, name):
    """The file or package a top-level import name resolves to here, or None."""
    if os.path.isfile(os.path.join(root, name + ".py")):
        return name + ".py"
    if os.path.isfile(os.path.join(root, name, "__init__.py")):
        return name
    return None


def imported_names(path):
    with open(path, encoding="utf-8") as f:
        tree = ast.parse(f.read(), path)
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for a in node.names:
                yield a.name.split(".")[0]
        elif isinstance(node, ast.ImportFrom) and node.level == 0 and node.module:
            yield node.module.split(".")[0]


def covered(target, sources):
    for s in sources:
        if s == "." or s == target or fnmatch.fnmatch(target, s):
            return True
        if target.startswith(s.rstrip("/") + "/"):  # copied as part of a directory
            return True
    return False


def main(root):
    dockerfile = os.path.join(root, "Dockerfile")
    if not os.path.isfile(dockerfile):
        print(f"{dockerfile}: no Dockerfile — nothing to check")
        return 0
    lines = dockerfile_lines(dockerfile)
    sources = copied_sources(lines)
    entry = entry_module(lines)
    if not os.path.isfile(os.path.join(root, entry)):
        print(f"entry module {entry} (from CMD) does not exist")
        return 1

    missing, seen, todo = [], {entry}, [entry]
    if not covered(entry, sources):
        missing.append((entry, "CMD"))
    while todo:
        path = todo.pop()
        file = os.path.join(root, path)
        if os.path.isdir(file):
            file = os.path.join(file, "__init__.py")
        for name in imported_names(file):
            target = local_target(root, name)
            if target is None or target in seen:
                continue
            seen.add(target)
            todo.append(target)
            if not covered(target, sources):
                missing.append((target, path))

    if missing:
        for target, by in missing:
            print(f"{target} is imported by {by} but no COPY puts it in the image")
        print(f"COPY sources: {' '.join(sources) or '(none)'}")
        return 1
    print(f"{len(seen)} local module(s) reached from {entry}, all copied")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else "."))
