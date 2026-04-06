import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer


TOKEN = os.environ["WEVE_BRIDGE_TOKEN"]
BRIDGE_ID = os.environ["WEVE_BRIDGE_BRIDGE_ID"]
TENANT_ID = os.environ["WEVE_BRIDGE_TENANT_ID"]


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/verify":
            self.send_response(404)
            self.end_headers()
            return

        if self.headers.get("Authorization") != f"Bearer {TOKEN}":
            self.send_response(401)
            self.end_headers()
            self.wfile.write(b"invalid token")
            return

        body = json.dumps({"tenantId": TENANT_ID, "bridgeId": BRIDGE_ID}).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        return


HTTPServer(("0.0.0.0", 8081), Handler).serve_forever()
