"""Piper voice loading, split out of server.py so it is testable in the gate
image, which has no fastapi, piper or torch — only the standard library
(check.sh: "keep the logic under test in a file that imports only the
standard library").

A model that fails to load used to be swallowed into a print —
`/health` said ok and the voice was simply absent. The deliberate choice
("a broken model must not take the service down") stays; the failure is no
longer invisible.
"""

import json
import os
import sys


def voice_id(filename: str) -> str:
    """ru_RU-denis-medium.onnx → "denis" — the short name callers address."""
    stem = filename[: -len(".onnx")]
    parts = stem.split("-")
    return parts[1] if len(parts) >= 2 else stem


def lang_of(filename: str) -> str:
    """ru_RU-denis-medium.onnx → "ru"; a name without an underscore carries
    no language → "" (before the fix "nora.onnx" reported the
    whole file name as its language)."""
    if "_" not in filename:
        return ""
    return filename.split("_", 1)[0]


def load_voices(models_dir: str, construct):
    """Load every *.onnx in models_dir via construct(path) → voice.

    Returns (voices, failed):
      voices  {voice_id: voice}
      failed  {filename: error text}

    construct is passed in rather than imported because it is the only part
    that touches piper; the loading policy here is pure standard library.
    """
    voices = {}
    failed = {}
    if not os.path.isdir(models_dir):
        return voices, failed
    served_from = {}  # voice_id → file name currently in `voices`
    for fn in sorted(os.listdir(models_dir)):
        if not fn.endswith(".onnx"):
            continue
        vid = voice_id(fn)
        try:
            v = construct(os.path.join(models_dir, fn))
        except Exception as e:  # a broken model must not take the service down
            failed[fn] = str(e)
            continue
        if vid in served_from:
            # Two qualities of one voice (ru_RU-denis-low/medium) map
            # to the same id and the last in sorted order silently won. The id
            # scheme stays — a quality suffix would change every
            # caller's voice ids — but the overwrite is no longer invisible: one line
            # with both file names and the one that wins, on stderr.
            print(f"WARNING: Piper voice_id collision for '{vid}': "
                  f"'{served_from[vid]}' and '{fn}' both map to it; "
                  f"serving '{fn}'", file=sys.stderr)
        voices[vid] = v
        served_from[vid] = fn
    return voices, failed


# gender for mounted models.

def load_voice_meta(models_dir: str) -> dict:
    """Read the voices.json beside the models, {voice_id: {"gender": …}}.

    Same file shape the cloning engines read from their samples directory
    (voices/voices.json). Missing or unreadable file → {}: dropping a model
    into PIPER_MODELS_DIR must not take the service down any more than a
    broken .onnx does, and the engine's fallback table covers the rest.
    """
    try:
        with open(os.path.join(models_dir, "voices.json")) as f:
            data = json.load(f)
    except (OSError, ValueError):
        return {}
    return data if isinstance(data, dict) else {}


def resolve_gender(voice_id: str, meta: dict, fallback: dict) -> str:
    """Gender of one voice: the voices.json entry wins, the engine's
    fallback table (the voices baked into the image) is second, unknown → "".
    """
    gender = (meta.get(voice_id) or {}).get("gender") or ""
    return gender or fallback.get(voice_id, "")
