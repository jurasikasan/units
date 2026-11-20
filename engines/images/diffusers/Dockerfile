# Build args:
#   ACCEL=xpu   →  Intel's PyTorch image (default): an Intel GPU, CPU when there is none
#   ACCEL=cuda  →  PyTorch's CUDA wheels, which bring their own CUDA runtime (the RTX box)
#
# docker build --build-arg ACCEL=cuda -t images-diffusers:cuda .
#
# No weights inside: the model directory is mounted (IMAGES_MODEL_DIR).

ARG ACCEL=xpu

FROM intel/intel-extension-for-pytorch:2.8.10-xpu AS base-xpu
COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt

FROM python:3.11-slim AS base-cuda
# --index-url, not --extra-index-url: with the latter pip may take PyPI's
# torch, and the container runs on CPU while looking like a GPU one.
RUN pip install --no-cache-dir --index-url https://download.pytorch.org/whl/cu126 torch==2.8.0
COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt

FROM base-${ACCEL} AS final
ENV PYTHONUNBUFFERED=1 HF_HUB_OFFLINE=1
WORKDIR /app
COPY server.py rules.py .
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=5s --start-period=300s --retries=3 \
    CMD python3 -c "import urllib.request; urllib.request.urlopen('http://localhost:8000/health')"
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8000"]
