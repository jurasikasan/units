"""WAV helpers for the Silero engine.

Imports only the standard library, on purpose: the check gate (check.sh) runs
on an image with no third-party packages, and a test that imports server.py
would drag in torch. Keep it that way — this file is exactly the kind of
dependency-free helper that check.sh is built to test.
"""

import io
import wave


def concat_wavs(wav_list: list[bytes]) -> bytes:
    """Concatenate PCM_16 WAV chunks into a single WAV.

    Every chunk must share the first chunk's nchannels, sampwidth and
    framerate; anything else raises ValueError. Trusting the first chunk's
    params for all of them (the old behaviour) silently mis-times audio the
    day a chunk is synthesized at different parameters.
    """
    if len(wav_list) == 1:
        return wav_list[0]
    params, frames = None, []
    for wav in wav_list:
        with wave.open(io.BytesIO(wav)) as w:
            p = w.getparams()
            if params is None:
                params = p
            else:
                for name in ("nchannels", "sampwidth", "framerate"):
                    if getattr(p, name) != getattr(params, name):
                        raise ValueError(
                            f"WAV chunk {name}={getattr(p, name)!r} differs "
                            f"from first chunk's {name}={getattr(params, name)!r}")
            frames.append(w.readframes(w.getnframes()))
    out = io.BytesIO()
    with wave.open(out, "wb") as w:
        w.setparams(params)
        for f in frames:
            w.writeframes(f)
    return out.getvalue()
