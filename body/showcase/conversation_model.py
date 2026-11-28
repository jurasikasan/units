"""A scripted mind for the conversation step: OpenAI's POST /v1/chat/completions,
answering every request with one thought and one tool call, and keeping a
summary of each request for the demo (GET /requests).

Which line a mind says next is read from what it was sent: the number of
speak_aloud calls among its recalled assistant messages. So the script
moves on only if the brain remembers its acts and recalls them as calls —
memory is part of what is shown. Past its lines, a mind calls skip_turn; so it does when the new moment is
only its body's act done by someone else (the demo's opening on A).
The model is the request's "model": a or b."""

import json
from http.server import BaseHTTPRequestHandler, HTTPServer

LINES = {
    "b": ["It is me, B. Who are you?", "Nice to meet you, A."],
    "a": ["Hello, I am the one holding an umbrella."],
}
# The hosts' step changes B's policy mid-conversation to another model with
# the same lines: it goes on from what B's memory says was already said.
LINES["b2"] = LINES["b"]
REQUESTS = []


def said(messages):
    return sum(1 for m in messages if m.get("role") == "assistant"
               for c in (m.get("tool_calls") or []) if c["function"]["name"] == "speak_aloud")


def summary(body):
    msgs = body["messages"]
    calls = {c["id"] for m in msgs if m.get("role") == "assistant" for c in (m.get("tool_calls") or [])}
    return {
        "model": body["model"],
        "tools": sorted(t["function"]["name"] for t in body.get("tools") or []),
        "tool_choice": body.get("tool_choice"),
        "roles": [m["role"] for m in msgs],
        "system": msgs[0]["content"] if msgs and msgs[0]["role"] == "system" else None,
        "said": said(msgs),
        # every tool message answers a call recalled before it
        "tool_messages": [{"tool_call_id": m["tool_call_id"], "answers_a_call": m["tool_call_id"] in calls,
                           "content": m["content"]} for m in msgs if m["role"] == "tool"],
        "last": msgs[-1]["content"] if isinstance(msgs[-1]["content"], str) else
        " | ".join(p.get("text", "") for p in msgs[-1]["content"]),
    }


def only_the_body(msg):
    """The new moment is only acts done through the body, not by this mind
    (the demo's opening on A): nothing heard, so nothing to answer."""
    parts = msg["content"] if isinstance(msg["content"], list) else [{"text": msg["content"]}]
    return all(" body] " in p.get("text", "") for p in parts)


def answer(body, k):
    who = body["model"]
    tools = {t["function"]["name"] for t in body.get("tools") or []}
    lines, n = LINES.get(who, []), said(body["messages"])
    if only_the_body(body["messages"][-1]):
        thought, call = "My body did that, not I; I wait.", {"name": "skip_turn", "arguments": "{}"}
    elif "speak_aloud" in tools and n < len(lines):
        thought, call = f"Someone spoke; I answer.", {"name": "speak_aloud", "arguments": json.dumps({"text": lines[n]})}
    else:
        thought, call = "Nothing more to say; I wait.", {"name": "skip_turn", "arguments": "{}"}
    return {"role": "assistant", "content": thought,
            "tool_calls": [{"id": f"call_{who}_{k}", "type": "function", "function": call}]}


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
        REQUESTS.append(summary(body))
        msg = answer(body, len(REQUESTS))
        print(f"model {body['model']}: heard {REQUESTS[-1]['last']!r}; calls {msg['tool_calls'][0]['function']}", flush=True)
        self._json(200, {"id": f"c-{len(REQUESTS)}", "object": "chat.completion", "model": body["model"],
                         "choices": [{"index": 0, "finish_reason": "tool_calls", "message": msg}]})

    def _json(self, code, obj):
        raw = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *args):
        pass


HTTPServer(("0.0.0.0", 8000), Door).serve_forever()
