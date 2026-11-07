# Build args:
#   GPU=false  →  python:3.11-slim (CPU, default, for local dev)
#   GPU=true   →  nvidia/cuda:12.1 (RTX, production)
#
# docker build --build-arg GPU=true -t stt-service:gpu .

ARG GPU=false

FROM python:3.11-slim AS base-false
RUN apt-get update && apt-get install -y ffmpeg && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir faster-whisper fastapi uvicorn python-multipart

FROM nvidia/cuda:12.1.0-cudnn8-runtime-ubuntu22.04 AS base-true
RUN apt-get update && apt-get install -y python3 python3-pip ffmpeg && rm -rf /var/lib/apt/lists/*
RUN pip3 install --no-cache-dir faster-whisper fastapi uvicorn python-multipart

FROM base-${GPU} AS final
WORKDIR /app
COPY server.py stt_rules.py .
EXPOSE 8001
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8001"]
