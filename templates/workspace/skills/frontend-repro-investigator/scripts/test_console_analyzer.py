#!/usr/bin/env python3
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("console_analyzer.py")


class ConsoleAnalyzerTest(unittest.TestCase):
    def run_script(self, content):
        with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False, encoding="utf-8") as f:
            if isinstance(content, str):
                f.write(content)
            else:
                json.dump(content, f)
            path = f.name
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--file", path],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )

    def test_extracts_api_failure_and_request_id_from_console_text(self):
        console_text = """
TypeError: Cannot read properties of undefined (reading 'id')
    at submitOrder (https://shop.example.com/assets/app.js:10:15)
    at HTMLButtonElement.onclick (https://shop.example.com/assets/app.js:20:5)
POST https://api.example.com/api/orders/42 500 (Internal Server Error)
x-request-id: req-123
"""

        res = self.run_script(console_text)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertIn("/api/orders/42", payload["backend_handoff"]["candidate_endpoints"])
        self.assertIn("req-123", payload["backend_handoff"]["trace_ids"])
        self.assertEqual(payload["frontend_findings"][0]["type"], "console_api_failure")

    def test_extracts_sentry_like_json_and_redacts_sensitive_values(self):
        sentry_event = {
            "request": {"url": "https://api.example.com/api/profile?token=secret"},
            "contexts": {"trace": {"trace_id": "trace-xyz"}},
            "extra": {"Authorization": "Bearer hidden"},
        }

        res = self.run_script(sentry_event)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        raw = res.stdout
        payload = json.loads(raw)
        self.assertIn("/api/profile", payload["backend_handoff"]["candidate_endpoints"])
        self.assertIn("trace-xyz", payload["backend_handoff"]["trace_ids"])
        self.assertNotIn("secret", raw)
        self.assertNotIn("hidden", raw)

    def test_redacts_multivalue_headers_sensitive_url_query_and_json_request_id(self):
        sentry_event = {
            "request": {
                "url": (
                    "https://api.example.com/api/profile?"
                    "secret_key=skey-987&access_token=token-987&ok=1"
                ),
                "headers": {
                    "x-request-id": "req-json-123",
                    "Cookie": "sid=first-cookie-secret-987; refresh=refresh-cookie-secret-987",
                    "Authorization": "Bearer bearer-hidden-987",
                },
            }
        }

        res = self.run_script(sentry_event)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        raw = res.stdout
        payload = json.loads(raw)
        self.assertIn("req-json-123", payload["backend_handoff"]["trace_ids"])
        self.assertNotIn("refresh-cookie-secret-987", raw)
        self.assertNotIn("secret_key=skey-987", raw)
        self.assertNotIn("access_token=token-987", raw)
        self.assertNotIn("bearer-hidden-987", raw)

    def test_redacts_full_sensitive_header_lines_from_console_text(self):
        console_text = """
Cookie: sid=first-cookie-text-987; refresh=refresh-cookie-secret-987
Set-Cookie: refresh=refresh-set-cookie-secret-987; Path=/; HttpOnly
X-Api-Key: api-key-hidden-987
Authorization: Bearer bearer-hidden-987
GET https://api.example.com/api/profile?secret_key=skey-987&access_token=token-987 500
"""

        res = self.run_script(console_text)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        raw = res.stdout
        self.assertNotIn("refresh-cookie-secret-987", raw)
        self.assertNotIn("refresh-set-cookie-secret-987", raw)
        self.assertNotIn("api-key-hidden-987", raw)
        self.assertNotIn("secret_key=skey-987", raw)
        self.assertNotIn("access_token=token-987", raw)
        self.assertNotIn("bearer-hidden-987", raw)

    def test_redacts_inline_sensitive_header_segments_from_console_text(self):
        console_text = """
headers: Cookie: sid=first-cookie-text-987; refresh=refresh-cookie-secret-987
metadata Authorization: Bearer bearer-hidden-987
"""

        res = self.run_script(console_text)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        raw = res.stdout
        self.assertNotIn("refresh-cookie-secret-987", raw)
        self.assertNotIn("bearer-hidden-987", raw)


if __name__ == "__main__":
    unittest.main()
