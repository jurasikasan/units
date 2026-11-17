"""The smallest AG-UI agent: it says back what it was told, a word at a time.

It stands in for any agent so that the face can be shown on its own; it reads
RunAgentInput and writes the events a text answer is made of.
"""

import json
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def text_of(message):
    content = message.get("content")
    if isinstance(content, str):
        return content
    return "\n".join(p.get("text", "") for p in content or [] if p.get("type") == "text")


class Agent(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/api/run":
            self.send_error(404)
            return
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        messages = body.get("messages") or []
        said = text_of(messages[-1]) if messages else ""
        thread, run, msg = body.get("threadId", ""), body.get("runId", ""), uuid.uuid4().hex

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()

        def send(**event):
            self.wfile.write(b"data: " + json.dumps(event).encode() + b"\n\n")
            self.wfile.flush()

        send(type="RUN_STARTED", threadId=thread, runId=run)
        send(type="TEXT_MESSAGE_START", messageId=msg, role="assistant")
        for word in f"You said ({len(messages)} messages so far): {said}".split(" "):
            send(type="TEXT_MESSAGE_CONTENT", messageId=msg, delta=word + " ")
            time.sleep(0.05)
        send(type="TEXT_MESSAGE_END", messageId=msg)
        send(type="RUN_FINISHED", threadId=thread, runId=run)


ThreadingHTTPServer(("", 8089), Agent).serve_forever()
