# syntax=docker/dockerfile:1
FROM python:3.10-slim

# Survive flaky pypi/pytorch downloads: retry + longer timeout. The BuildKit
# cache mount keeps the (large) torch wheel across builds — including
# `--no-cache` rebuilds, which invalidate layers but NOT cache mounts.
# (No --no-cache-dir here on purpose: that would defeat the cache mount.)
# --extra-index-url (NOT --index-url): the pytorch index supplies the +cpu torch
# wheel, PyPI stays available for everything else.
ENV PIP_DEFAULT_TIMEOUT=120 PIP_RETRIES=5
COPY requirements.txt constraints.txt /tmp/
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install -r /tmp/requirements.txt -c /tmp/constraints.txt \
        --extra-index-url https://download.pytorch.org/whl/cpu

WORKDIR /app
COPY server.py vad_rules.py .

EXPOSE 8096
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8096"]
