"""Pure rules for the VAD service: request-body validation and env tuning.

Standard library only, so the gate (python3, nothing else) can import and
test it, and the container image carries the same decisions.
"""

# The caller sends exactly 512 samples of 16-bit PCM per
# request: 512 * 2 = 1024 bytes. Anything else is rejected with a 400.
EXPECTED_SAMPLES = 512
EXPECTED_BODY_BYTES = EXPECTED_SAMPLES * 2

DEFAULT_SESSION_TTL = 300        # seconds of idle before a session is evicted
DEFAULT_SWEEP_SECONDS = 60      # how often the cleanup loop runs

ENV_SESSION_TTL = "VAD_SESSION_TTL"
ENV_SWEEP_SECONDS = "VAD_SWEEP_SECONDS"


def validate_body_length(body):
    """Return None if body is a valid VAD window, else a 400 error message.

    Valid means exactly EXPECTED_BODY_BYTES (1024 bytes = 512 int16 samples);
    an odd length would also raise inside np.frombuffer, so it must be
    rejected here, before numpy or the model is touched.
    """
    if len(body) == EXPECTED_BODY_BYTES:
        return None
    return (
        f"audio body must be exactly {EXPECTED_BODY_BYTES} bytes "
        f"({EXPECTED_SAMPLES} int16 samples), got {len(body)} bytes"
    )


def _parse_positive_int(value, default: int) -> int:
    """Parse value as a positive int; fall back to default on any problem."""
    if value is None:
        return default
    try:
        n = int(str(value).strip())
    except (TypeError, ValueError):
        return default
    return n if n > 0 else default


def session_ttl(env) -> int:
    """Session TTL in seconds from env, default DEFAULT_SESSION_TTL."""
    return _parse_positive_int(env.get(ENV_SESSION_TTL), DEFAULT_SESSION_TTL)


def sweep_seconds(env) -> int:
    """Cleanup sweep period in seconds from env, default DEFAULT_SWEEP_SECONDS."""
    return _parse_positive_int(env.get(ENV_SWEEP_SECONDS), DEFAULT_SWEEP_SECONDS)
