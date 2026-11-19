"""A chat model that answers with what it was told: OpenAI's
/v1/chat/completions, streamed, a word at a time. The showcase's model, so
the call shows that what hearing heard reached the model and came back."""
import json
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Model(BaseHTTPRequestHandler):
    def do_POST(self):
        req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        heard = next((m["content"] for m in reversed(req["messages"]) if m["role"] == "user"), "")
        answer = f"You said: {heard} Now a few more sentences, so that there is time to talk over me. One. Two. Three. Four."
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for word in answer.split(" "):
            chunk = {"choices": [{"index": 0, "delta": {"content": word + " "}}]}
            self.wfile.write(f"data: {json.dumps(chunk)}\n\n".encode())
            self.wfile.flush()
            time.sleep(0.05)
        self.wfile.write(b"data: [DONE]\n\n")

    def log_message(self, *args):
        pass


ThreadingHTTPServer(("0.0.0.0", 8000), Model).serve_forever()
