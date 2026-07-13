#!/usr/bin/env python3
from __future__ import annotations

import json
import os
import subprocess
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


SCRIPT = Path(__file__).with_name("sentry_fetch.py")


class SentryHandler(BaseHTTPRequestHandler):
    request_path = ""
    authorization = ""
    response_mode = "ok"

    def do_GET(self):
        SentryHandler.request_path = self.path
        SentryHandler.authorization = self.headers.get("Authorization", "")
        if SentryHandler.authorization != "Bearer token-123":
            self.send_response(401)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"detail": "unauthorized"}).encode("utf-8"))
            return
        if SentryHandler.response_mode == "http_500":
            self.send_response(500)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"detail": "server failed"}).encode("utf-8"))
            return
        if SentryHandler.response_mode == "bad_json":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b"{bad json")
            return
        if SentryHandler.response_mode == "array_json":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps([{"eventID": "event-1"}]).encode("utf-8"))
            return

        payload = {
            "eventID": "event-1",
            "message": "Profile load failed secret_key=plain-secret session=session-secret Cookie: sid=cookie-secret",
            "request": {
                "url": "https://api.example.com/api/profile?access_token=hidden-token&session=session-token&ok=1",
                "headers": [
                    {"key": "Authorization", "value": "Bearer hidden-token"},
                    {"key": "x-request-id", "value": "req-1"},
                    ["traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"],
                    ["Cookie", "sid=list-cookie-secret"],
                    ["X-Api-Key", "list-api-key-secret"],
                    {"name": "Cookie", "value": "sid=cookie-secret"},
                ],
            },
            "contexts": {"trace": {"trace_id": "trace-1"}},
            "session": "nested-session-secret",
            "extra": {
                "api_key": "api-key-secret",
                "plain": "token=plain-token secret_key=plain-secret session=plain-session",
                "ignored_url": "https://app.example.com/foo/api/not-real",
            },
            "exception": {
                "values": [
                    {
                        "type": "TypeError",
                        "value": "cannot read profile",
                        "stacktrace": {
                            "frames": [
                                {
                                    "function": "loadProfile",
                                    "filename": "https://app.example.com/assets/app.js",
                                    "lineno": 42,
                                }
                            ]
                        },
                    }
                ]
            },
        }
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(payload).encode("utf-8"))

    def log_message(self, fmt, *args):
        return


class SentryFetchTest(unittest.TestCase):
    def setUp(self):
        SentryHandler.response_mode = "ok"
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), SentryHandler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.base_url = f"http://127.0.0.1:{self.server.server_port}"

    def tearDown(self):
        self.server.shutdown()
        self.thread.join(timeout=5)
        self.server.server_close()

    def run_script(self, env=None):
        merged_env = os.environ.copy()
        merged_env.pop("SENTRY_AUTH_TOKEN", None)
        if env:
            merged_env.update(env)
        return subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--base-url",
                self.base_url,
                "--organization",
                "acme",
                "--project",
                "web",
                "--event-id",
                "event-1",
            ],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
            env=merged_env,
        )

    def test_fetches_event_and_outputs_normalized_redacted_handoff(self):
        res = self.run_script({"SENTRY_AUTH_TOKEN": "token-123"})

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        self.assertEqual(
            SentryHandler.request_path,
            "/api/0/projects/acme/web/events/event-1/",
        )
        self.assertEqual(SentryHandler.authorization, "Bearer token-123")
        payload = json.loads(res.stdout)
        self.assertIn("/api/profile", payload["backend_handoff"]["candidate_endpoints"])
        self.assertIn("trace-1", payload["backend_handoff"]["trace_ids"])
        self.assertIn("req-1", payload["backend_handoff"]["trace_ids"])
        self.assertIn("4bf92f3577b34da6a3ce929d0e0e4736", payload["backend_handoff"]["trace_ids"])
        self.assertNotIn("/foo/api/not-real", payload["backend_handoff"]["candidate_endpoints"])
        self.assertNotIn("/api/not-real", payload["backend_handoff"]["candidate_endpoints"])
        self.assertEqual(payload["summary"]["event_id"], "event-1")
        for secret in [
            "hidden-token",
            "plain-secret",
            "session-secret",
            "session-token",
            "cookie-secret",
            "nested-session-secret",
            "api-key-secret",
            "list-cookie-secret",
            "list-api-key-secret",
            "plain-token",
            "plain-session",
        ]:
            self.assertNotIn(secret, res.stdout)

    def test_missing_env_token_returns_json_error_code_2(self):
        res = self.run_script()

        self.assertEqual(res.returncode, 2, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertEqual(payload["error"]["code"], 2)
        self.assertIn("SENTRY_AUTH_TOKEN", res.stdout)
        self.assertEqual(res.stderr, "")

    def test_fetch_failures_return_json_code_3_without_traceback_or_token_leak(self):
        for mode in ("http_500", "bad_json", "array_json"):
            with self.subTest(mode=mode):
                SentryHandler.response_mode = mode

                res = self.run_script({"SENTRY_AUTH_TOKEN": "token-123"})

                self.assertEqual(res.returncode, 3, res.stderr + res.stdout)
                payload = json.loads(res.stdout)
                self.assertEqual(payload["error"]["code"], 3)
                self.assertEqual(res.stderr, "")
                self.assertNotIn("Traceback", res.stdout)
                self.assertNotIn("token-123", res.stdout)

    def test_malformed_base_url_returns_json_code_3_without_stderr_or_secret_leak(self):
        env = os.environ.copy()
        env.pop("SENTRY_AUTH_TOKEN", None)
        res = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--base-url",
                "http://bad host/access_token=base-secret",
                "--organization",
                "acme",
                "--project",
                "web",
                "--event-id",
                "event-1",
                "--token",
                "token-123",
            ],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
            env=env,
        )

        self.assertEqual(res.returncode, 3, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertEqual(payload["error"]["code"], 3)
        self.assertEqual(res.stderr, "")
        self.assertNotIn("Traceback", res.stdout)
        self.assertNotIn("base-secret", res.stdout)
        self.assertNotIn("token-123", res.stdout)


if __name__ == "__main__":
    unittest.main()
