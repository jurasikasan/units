"""The Silero voice table, kept importable without torch.

server.py builds its VOICES from here at import time; the gate's tests import
this file directly, so it must stay stdlib-only (the gate's image has no
torch, no fastapi).

A SILERO_SPEAKER_EN equal to a Russian voice id used to rewrite that Russian
voice in place: the id survived in /voices but was then routed to the English
model, with no validation and no warning. build_voices now refuses instead.

The English speaker is anonymous in the engine, so its gender could not be
annotated either: /voices always reported "" for it and a UI picker filtering
by gender never showed it. SILERO_GENDER_EN (default empty) supplies it — the
speaker's gender is only known after someone listens to it, so it is set in
the deployment's .env, not guessed here.
"""

import os

ENV_NAME = "SILERO_SPEAKER_EN"
DEFAULT_EN_SPEAKER = "en_0"
GENDER_ENV_NAME = "SILERO_GENDER_EN"

# The five Russian voices of v4_ru. Genders are hand-annotated because the
# engine does not report them — this table is the right place for that, next
# to the engine rather than in a caller's config.
RU_VOICES = {
    "aidar":   {"lang": "ru", "gender": "male"},
    "baya":    {"lang": "ru", "gender": "female"},
    "kseniya": {"lang": "ru", "gender": "female"},
    "xenia":   {"lang": "ru", "gender": "female"},
    "eugene":  {"lang": "ru", "gender": "male"},
}


def build_voices(en_speaker: str) -> dict:
    """Return the voice table for the given v3_en speaker.

    Raises ValueError if en_speaker is a Russian voice id: the table would
    keep that id in /voices while routing it to the English model, which is
    exactly the silent failure this guard exists to make fatal.
    """
    if en_speaker in RU_VOICES:
        raise ValueError(
            f"{ENV_NAME}={en_speaker!r} collides with the Russian voice "
            f"'{en_speaker}': it would stay listed in /voices but be "
            f"synthesized by the English model. Set {ENV_NAME} to an en_* "
            f"speaker id instead."
        )
    voices = dict(RU_VOICES)
    # Same "male"/"female" values the Russian voices use; empty when unset,
    # which is what /voices reported before this variable existed.
    voices[en_speaker] = {"lang": "en", "gender": os.getenv(GENDER_ENV_NAME, "")}
    return voices
