#!/usr/bin/env python3
"""A stand-in for AlertLoop's ingestion endpoint, for testing the adapter.

It records every request it receives and answers with whatever the test asked
for. Deliberately dumb: the point is to exercise the adapter's behaviour on
each response, not to reimplement AlertLoop.

  mock-alertloop.py <port> <requests-file> <control-file>

The control file holds one response instruction per line, consumed in order:

  200            answer 200 with an empty JSON object
  500            answer 500
  429            answer 429
  hang           accept the connection and never answer (tests the timeout)
  403:<code>     answer 403 (any status) with AlertLoop's error body, error.code <code>

When the instructions run out, the last one repeats. Each request is appended to
the requests file as one JSON object per line: method, path, headers, body.
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(sys.argv[1])
REQUESTS_FILE = sys.argv[2]
CONTROL_FILE = sys.argv[3]

def next_instruction():
    """Take the next instruction, consuming it from the control file.

    Deliberately stateless: an in-process counter would keep counting across the
    control file being rewritten between tests, so a test that asks for
    "500, 500, 200" would silently get whatever the previous test left the
    counter pointing at. The file is the only state.

    The last instruction is never consumed, so it repeats - which is what
    "answer 500 to everything" needs.
    """
    try:
        with open(CONTROL_FILE, encoding="utf-8") as fh:
            lines = [ln.strip() for ln in fh if ln.strip()]
    except FileNotFoundError:
        lines = []
    if not lines:
        return "200"
    if len(lines) > 1:
        with open(CONTROL_FILE, "w", encoding="utf-8") as fh:
            fh.writelines(line + chr(10) for line in lines[1:])
    return lines[0]


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self):  # noqa: N802 - name required by BaseHTTPRequestHandler
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length).decode("utf-8", "replace")

        with open(REQUESTS_FILE, "a", encoding="utf-8") as fh:
            fh.write(json.dumps({
                "method": self.command,
                "path": self.path,
                "headers": {k.lower(): v for k, v in self.headers.items()},
                "body": body,
            }) + "\n")

        instruction = next_instruction()
        if instruction == "hang":
            # Never answer. The adapter's --max-time is what has to end this.
            time.sleep(60)
            return

        instruction, _, error_code = instruction.partition(":")
        try:
            code = int(instruction)
        except ValueError:
            code = 200

        payload = b'{"id":"test-event-id"}'
        if error_code:
            payload = json.dumps({"error": {"code": error_code, "message": "refused"}}).encode()
        if code == 204:
            payload = b""
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        if payload:
            self.wfile.write(payload)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
