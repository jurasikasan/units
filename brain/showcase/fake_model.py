"""A model door that thinks nothing: OpenAI's POST /v1/chat/completions,
answering with what it was given, and keeping a summary of every request
for the demo to read (GET /requests). The showcase is about the brain's
walls, not about a model's mind.

A picture is summarised by the sha256 of the bytes its data: URL carries,
so the demo can tell the frame it put in artifact_store reached the model
as bytes, not as a reference into our stores."""

import base64
import hashlib
import json
from http.server import BaseHTTPRequestHandler, HTTPServer

REQUESTS = []


def summary(body):
    msgs = []
    for m in body["messages"]:
        content = m["content"]
        if isinstance(content, str):
            msgs.append({"role": m["role"], "texts": [content], "images": []})
            continue
        texts, images = [], []
        for p in content:
            if p["type"] == "text":
                texts.append(p["text"])
            elif p["type"] == "image_url":
                url = p["image_url"]["url"]
                head, _, data = url.partition(",")
                images.append({"head": head, "sha256": hashlib.sha256(base64.b64decode(data)).hexdigest()})
        msgs.append({"role": m["role"], "texts": texts, "images": images})
    return {"model": body["model"], "messages": msgs}


class Door(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self._json(200, {"status": "ok"})
        elif self.path == "/requests":
            self._json(200, REQUESTS)
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self._json(404, {"error": "not found"})
            return
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        s = summary(body)
        REQUESTS.append(s)
        last = s["messages"][-1]
        answer = "I was given: " + " | ".join(last["texts"]) + f" and {len(last['images'])} picture(s)."
        print(f"model: {len(s['messages'])} messages; answered: {answer}", flush=True)
        self._json(200, {"choices": [{"index": 0, "message": {"role": "assistant", "content": answer}}]})

    def _json(self, code, obj):
        raw = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *_):
        pass


HTTPServer(("0.0.0.0", 8000), Door).serve_forever()
