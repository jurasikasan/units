"""A file server for the showcase: GET what is there, PUT keeps the body.
Every request's path, method and Authorization header is logged, so the demo
can tell which secret went where."""
import http.server
import json

FILES = {"/v1/files/greeting.txt": b"hello from outside\n"}
SEEN = []


class H(http.server.BaseHTTPRequestHandler):
    def _seen(self):
        SEEN.append({"method": self.command, "path": self.path, "auth": self.headers.get("Authorization", "")})

    def do_GET(self):
        if self.path == "/_seen":
            body = json.dumps(SEEN).encode()
        elif self.path in FILES:
            self._seen()
            body = FILES[self.path]
        else:
            self._seen()
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_PUT(self):
        self._seen()
        n = int(self.headers.get("Content-Length", "0"))
        FILES[self.path] = self.rfile.read(n)
        self.send_response(201)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def log_message(self, *a):
        pass


http.server.ThreadingHTTPServer(("", 8000), H).serve_forever()
