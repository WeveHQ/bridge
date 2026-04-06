from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def _send(self):
        self.send_response(201)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"source":"docker"}')

    def do_GET(self):
        self._send()

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self._send()

    def log_message(self, format, *args):
        return


HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
