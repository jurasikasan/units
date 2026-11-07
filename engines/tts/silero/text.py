"""Pure text helpers for the Silero engine.

Imports only the standard library, on purpose: the check gate (check.sh) runs
on an image with no third-party packages, and a test that imports server.py
would drag in torch. Keep it that way — if a helper needs a runtime
dependency, it belongs in server.py, not here.
"""

import re

MAX_CHARS = 200  # safe margin under Silero's limit; the real limit is unmeasured


def chunk(text: str, max_chars: int = MAX_CHARS) -> list[str]:
    """Split text into chunks of at most max_chars.

    Prefers sentence boundaries; a sentence longer than max_chars is split at
    word boundaries so no chunk ends mid-word (the only exception is a single
    token longer than max_chars, which has no boundary to cut at).
    """
    chunks, current = [], ""
    for sentence in re.split(r"(?<=[.!?…])\s+", text):
        sentence = sentence.strip()
        if not sentence:
            continue
        if len(current) + len(sentence) + 1 <= max_chars:
            current = (current + " " + sentence).strip()
        else:
            if current:
                chunks.append(current)
            parts = _split_long(sentence, max_chars)
            chunks.extend(parts[:-1])
            current = parts[-1]
    if current:
        chunks.append(current)
    return chunks


def _split_long(sentence: str, max_chars: int) -> list[str]:
    """Split an over-limit sentence at word boundaries.

    Greedy: cut at the last space strictly before max_chars. Joining the
    parts with a single space reproduces the input.
    """
    parts = []
    while len(sentence) > max_chars:
        cut = sentence.rfind(" ", 0, max_chars)
        if cut <= 0:
            cut = max_chars  # single token longer than max_chars — no boundary exists
        parts.append(sentence[:cut])
        sentence = sentence[cut:].lstrip(" ")
    if sentence:
        parts.append(sentence)
    return parts
