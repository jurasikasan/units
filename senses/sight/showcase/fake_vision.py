"""A vision door that sees without a model: OpenAI's POST /v1/chat/completions,
answering every picture with the same two lines, and saying what it was sent.
The showcase is about sight's wire and events, not about a model's eyes."""

import json
from http.server import BaseHTTPRequestHandler, HTTPServer

ANSWER = "A stone corridor lit by one torch; someone stands at its left edge.\nRead: KEEP OUT"


class Door(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self._json(200, {"status": "ok"})
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self._json(404, {"error": "not found"})
            return
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        parts = body["messages"][0]["content"]
        image = next(p["image_url"]["url"] for p in parts if p["type"] == "image_url")
        print(f"vision: model={body['model']} picture={image[:30]}… ({len(image)} chars)", flush=True)
        self._json(200, {"choices": [{"index": 0, "message": {"role": "assistant", "content": ANSWER}}]})

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
