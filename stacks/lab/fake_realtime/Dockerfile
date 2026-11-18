# A realtime model that answers from a script (fake_realtime.py, README.md).
FROM python:3.13-alpine
RUN pip install --no-cache-dir websockets==15.0.1
WORKDIR /app
COPY fake_realtime.py .
USER nobody
EXPOSE 8090
CMD ["python", "-u", "fake_realtime.py"]
