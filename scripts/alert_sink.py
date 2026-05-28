import json
import time
import logging
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


ALERTS = []
MAX_ALERTS = 100


class AlertSinkHandler(BaseHTTPRequestHandler):
    def _send_json(self, status, payload):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path in ("/", "/health"):
            self._send_json(200, {"status": "ok", "alerts": len(ALERTS)})
            return
        if self.path == "/alerts":
            self._send_json(200, {"alerts": ALERTS})
            return
        self._send_json(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/webhook":
            self._send_json(404, {"error": "not found"})
            return

        body_len = int(self.headers.get("Content-Length", "0"))
        raw_body = self.rfile.read(body_len) if body_len else b"{}"
        payload = json.loads(raw_body.decode("utf-8") or "{}")
        entry = {
            "received_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "group_key": payload.get("groupKey"),
            "status": payload.get("status"),
            "receiver": payload.get("receiver"),
            "alerts": payload.get("alerts", []),
        }
        ALERTS.insert(0, entry)
        del ALERTS[MAX_ALERTS:]
        self._send_json(200, {"stored": len(entry["alerts"])})

    def log_message(self, fmt, *args):
        return


def main():
    server = ThreadingHTTPServer(("0.0.0.0", 8088), AlertSinkHandler)
    logging.basicConfig(level=logging.INFO)
    logging.getLogger(__name__).info("alert-sink listening on :8088")
    server.serve_forever()


if __name__ == "__main__":
    main()
