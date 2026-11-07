# Build args:
#   ACCEL=xpu   →  Intel IPEX image (default; the server falls back to CPU when
#                  no Intel GPU is present, so this also covers plain CPU hosts)
#   ACCEL=cuda  →  nvidia/cuda + CUDA torch (the RTX box)
#
# docker build --build-arg ACCEL=cuda -t tts-service:cuda .

ARG ACCEL=xpu

FROM intel/intel-extension-for-pytorch:2.8.10-xpu AS base-xpu
RUN apt-get update && apt-get install -y libsndfile1 && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir fastapi uvicorn soundfile omegaconf

FROM nvidia/cuda:12.4.1-cudnn-runtime-ubuntu22.04 AS base-cuda
RUN apt-get update && apt-get install -y python3 python3-pip libsndfile1 \
    && rm -rf /var/lib/apt/lists/*
# --index-url, not --extra-index-url: with the latter pip is free to resolve an
# unpinned torch from PyPI, which is the CPU build, and the container then runs
# on CPU while looking like a GPU deployment.
RUN pip3 install --no-cache-dir --index-url https://download.pytorch.org/whl/cu124 \
    torch torchaudio
RUN pip3 install --no-cache-dir fastapi uvicorn soundfile omegaconf

FROM base-${ACCEL} AS final
WORKDIR /app
COPY server.py text.py voices.py wavutil.py .
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 \
    CMD python3 -c "import urllib.request; urllib.request.urlopen('http://localhost:8000/health')"
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8000"]
