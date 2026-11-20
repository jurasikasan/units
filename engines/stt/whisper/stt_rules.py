"""Request rules for the STT service, standard library only so check.sh can test them."""


class TooLarge(Exception):
    pass


def max_upload_bytes(env) -> int:
    """STT_MAX_UPLOAD_BYTES, default 25 MiB: the most Groq's endpoint takes, so a
    clip one backend accepts is never refused by another. A bad value falls back."""
    raw = env.get("STT_MAX_UPLOAD_BYTES", "")
    try:
        n = int(raw)
    except ValueError:
        return 25 * 1024 * 1024
    return n if n > 0 else 25 * 1024 * 1024


def read_capped(fileobj, limit: int) -> bytes:
    """Reads at most limit bytes; one byte more means the upload is over the
    limit, and nothing has been written anywhere yet."""
    data = fileobj.read(limit + 1)
    if len(data) > limit:
        raise TooLarge(f"upload exceeds {limit} bytes")
    return data


def audio_fields(audio_s, elapsed_ms: float) -> str:
    """The END line's audio length and real-time factor. 0.0 is a length, not a
    missing one; only None (no duration known) drops the fields."""
    if audio_s is None:
        return ""
    out = f" audio={audio_s:.2f}s"
    if audio_s > 0:
        out += f" RTF={elapsed_ms / 1000 / audio_s:.2f}x"
    return out


def wait_turn(slot, gone, poll_s: float = 0.25) -> bool:
    """Takes slot (a threading.Semaphore) for one decode. A caller that leaves
    while it waits never decodes: False, and the slot is not held. True means
    the slot is held and must be released."""
    while not slot.acquire(timeout=poll_s):
        if gone():
            return False
    if gone():
        slot.release()
        return False
    return True


def collect(segments, gone):
    """Pulls segments from faster-whisper's lazy generator, asking gone()
    before each next one: decoding happens inside next(), so a caller that left
    stops the work at the next segment. Returns (segments, cancelled); on cancel
    the generator is closed and nothing more is pulled from it."""
    out = []
    it = iter(segments)
    try:
        while True:
            if gone():
                return out, True
            try:
                out.append(next(it))
            except StopIteration:
                return out, False
    finally:
        close = getattr(it, "close", None)
        if close:
            close()
