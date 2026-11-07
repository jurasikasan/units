FROM python:3.11-slim
WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends wget ca-certificates \
    && rm -rf /var/lib/apt/lists/*
# Versions pinned 2026-10-30. server.py
# calls the 1.8 API directly; a piper-tts bump is a code change, not a rebuild.
RUN pip install --no-cache-dir piper-tts==1.8.0 fastapi==0.162.0 uvicorn==0.54.0

# Bake the voices in (~65 MB each) so a cold container needs no network.
# lessac is English, for the SMS→voice demo's announcer.
ARG PIPER_BASE=https://huggingface.co/rhasspy/piper-voices/resolve/main
RUN mkdir -p /models && for lv in ru_RU/denis ru_RU/dmitri ru_RU/irina ru_RU/ruslan en_US/lessac; do \
      loc=${lv%/*}; v=${lv#*/}; dir="$PIPER_BASE/${loc%_*}/$loc/$v/medium"; \
      wget -q "$dir/$loc-$v-medium.onnx"      -O "/models/$loc-$v-medium.onnx" && \
      wget -q "$dir/$loc-$v-medium.onnx.json" -O "/models/$loc-$v-medium.onnx.json" || exit 1; \
    done

ENV PIPER_MODELS_DIR=/models

COPY server.py loader.py .
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD python -c "import urllib.request;urllib.request.urlopen('http://localhost:8000/health').read()"
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8000"]
