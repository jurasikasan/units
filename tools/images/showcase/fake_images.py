"""An OpenAI Images endpoint that draws without a cloud: any prompt is a
64x64 PNG, one colour picked from the prompt's words; a prompt with "spent"
in it is refused as a spent allowance (429), in OpenAI's error shape."""

import base64
import json
import struct
import zlib
from http.server import BaseHTTPRequestHandler, HTTPServer

COLOURS = {"red": (220, 40, 40), "green": (40, 170, 70), "blue": (40, 90, 220)}


def png(rgb, side=64):
    def chunk(kind, data):
        c = kind + data
        return struct.pack(">I", len(data)) + c + struct.pack(">I", zlib.crc32(c) & 0xFFFFFFFF)

    row = b"\x00" + bytes(rgb) * side
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", side, side, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(row * side)) + chunk(b"IEND", b""))


class Images(BaseHTTPRequestHandler):
    def do_GET(self):
        self.answer(200, {"status": "ok"})

    def do_POST(self):
        if self.path != "/v1/images/generations":
            return self.answer(404, {"error": {"message": "no such route"}})
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        prompt = body.get("prompt", "")
        print(f"asked: model={body.get('model')} size={body.get('size')} prompt={prompt!r}", flush=True)
        if "spent" in prompt:
            return self.answer(429, {"error": {"message": "the daily allowance is spent", "type": "rate_limit"}})
        rgb = next((c for w, c in COLOURS.items() if w in prompt.lower()), (128, 128, 128))
        self.answer(200, {"created": 0, "size": "64x64", "data": [{"b64_json": base64.b64encode(png(rgb)).decode()}]})

    def answer(self, status, obj):
        b = json.dumps(obj).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def log_message(self, *_):
        pass


HTTPServer(("", 8000), Images).serve_forever()
