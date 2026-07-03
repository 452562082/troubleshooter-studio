#!/usr/bin/env python3
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("har_analyzer.py")


def entry(url, status=200, method="GET", ms=10, request_headers=None, response_headers=None, body=""):
    return {
        "startedDateTime": "2026-07-03T10:00:00.000+08:00",
        "time": ms,
        "request": {
            "method": method,
            "url": url,
            "headers": [{"name": k, "value": v} for k, v in (request_headers or {}).items()],
        },
        "response": {
            "status": status,
            "statusText": "ERR" if status >= 400 else "OK",
            "headers": [{"name": k, "value": v} for k, v in (response_headers or {}).items()],
            "content": {"text": body, "mimeType": "application/json"},
        },
        "timings": {"blocked": 0, "dns": 0, "connect": 0, "send": 1, "wait": max(ms - 2, 0), "receive": 1},
    }


class HARAnalyzerTest(unittest.TestCase):
    def run_script(self, har):
        with tempfile.NamedTemporaryFile("w", suffix=".har", delete=False, encoding="utf-8") as f:
            json.dump(har, f)
            path = f.name
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--file", path],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )

    def test_extracts_failed_api_and_trace_headers(self):
        har = {"log": {"entries": [
            entry("https://static.example.com/app.js", 200),
            entry(
                "https://api.example.com/api/orders/42",
                status=500,
                method="POST",
                ms=860,
                request_headers={"x-request-id": "req-1"},
                response_headers={"x-trace-id": "trace-abc"},
                body='{"error":"db timeout"}',
            ),
        ]}}

        res = self.run_script(har)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertEqual(payload["summary"]["failed_request_count"], 1)
        self.assertEqual(payload["failed_requests"][0]["method"], "POST")
        self.assertEqual(payload["failed_requests"][0]["status"], 500)
        self.assertEqual(payload["failed_requests"][0]["trace_ids"], ["req-1", "trace-abc"])
        self.assertIn("/api/orders/42", payload["backend_handoff"]["candidate_endpoints"])

    def test_detects_static_chunk_failure(self):
        har = {"log": {"entries": [
            entry("https://shop.example.com/assets/chunk-abc.js", status=404, body="not found"),
        ]}}

        res = self.run_script(har)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertEqual(payload["frontend_findings"][0]["type"], "static_asset_failed")
        self.assertEqual(payload["backend_handoff"]["candidate_endpoints"], [])

    def test_reports_slow_requests_without_5xx(self):
        har = {"log": {"entries": [
            entry("https://api.example.com/api/search?q=x", status=200, ms=2500),
        ]}}

        res = self.run_script(har)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertEqual(payload["summary"]["slow_request_count"], 1)
        self.assertEqual(payload["slow_requests"][0]["duration_ms"], 2500)


if __name__ == "__main__":
    unittest.main()
