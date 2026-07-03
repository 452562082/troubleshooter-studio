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
        types = [item["type"] for item in payload["frontend_findings"]]
        self.assertIn("static_asset_failed", types)
        self.assertEqual(payload["backend_handoff"]["candidate_endpoints"], [])

    def test_network_abort_static_asset_stays_out_of_backend_handoff(self):
        har = {"log": {"entries": [
            entry("https://shop.example.com/assets/chunk-deadbeef.js", status=0),
            entry("https://shop.example.com/api/static/app.js", status=0),
        ]}}

        res = self.run_script(har)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        types = [item["type"] for item in payload["frontend_findings"]]
        self.assertIn("network_request_aborted", types)
        self.assertEqual(payload["backend_handoff"]["candidate_endpoints"], [])

    def test_detects_chunk_source_map_cache_csp_and_mixed_content_evidence(self):
        har = {"log": {"entries": [
            entry(
                "https://shop.example.com/assets/chunk-abc12345.js",
                status=404,
                response_headers={"Age": "3600"},
            ),
            entry("https://shop.example.com/assets/app.js.map", status=404),
            entry(
                "https://shop.example.com/",
                status=200,
                response_headers={"Content-Security-Policy": "default-src 'self'; connect-src https://api.example.com"},
            ),
            entry("http://api.example.com/api/profile", status=0),
        ]}}

        res = self.run_script(har)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        types = [item["type"] for item in payload["frontend_findings"]]
        self.assertIn("chunk_load_error", types)
        self.assertIn("asset_cache_risk", types)
        self.assertIn("source_map_missing", types)
        self.assertIn("csp_present", types)
        self.assertIn("mixed_content_or_tls_block", types)
        self.assertIn("/api/profile", payload["backend_handoff"]["candidate_endpoints"])

    def test_reports_slow_requests_without_5xx(self):
        har = {"log": {"entries": [
            entry("https://api.example.com/api/search?q=x", status=200, ms=2500),
        ]}}

        res = self.run_script(har)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertEqual(payload["summary"]["slow_request_count"], 1)
        self.assertEqual(payload["slow_requests"][0]["duration_ms"], 2500)

    def test_detects_cors_preflight_and_network_abort(self):
        har = {"log": {"entries": [
            entry("https://api.example.com/api/orders", status=403, method="OPTIONS", response_headers={}, body="cors denied"),
            entry("https://api.example.com/api/payments", status=0, method="POST", ms=120),
        ]}}

        res = self.run_script(har)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        types = [item["type"] for item in payload["frontend_findings"]]
        self.assertIn("cors_preflight_failed", types)
        self.assertIn("network_request_aborted", types)
        self.assertIn("/api/payments", payload["backend_handoff"]["candidate_endpoints"])

    def test_extracts_trace_id_from_traceparent(self):
        har = {"log": {"entries": [
            entry(
                "https://api.example.com/api/orders/42",
                status=500,
                request_headers={"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
            ),
        ]}}

        res = self.run_script(har)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertIn("4bf92f3577b34da6a3ce929d0e0e4736", payload["backend_handoff"]["trace_ids"])

    def test_detects_graphql_error_and_auth_redirect(self):
        har = {"log": {"entries": [
            entry("https://api.example.com/graphql", status=200, method="POST", body='{"errors":[{"message":"resolver failed"}]}'),
            entry("https://api.example.com/api/profile", status=302, response_headers={"Location": "https://login.example.com/sso"}),
        ]}}

        res = self.run_script(har)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        types = [item["type"] for item in payload["frontend_findings"]]
        self.assertIn("graphql_error_response", types)
        self.assertIn("auth_redirect", types)
        self.assertIn("/graphql", payload["backend_handoff"]["candidate_endpoints"])
        self.assertIn("/api/profile", payload["backend_handoff"]["candidate_endpoints"])

    def test_redacts_sensitive_output_values(self):
        har = {"log": {"entries": [
            entry(
                "https://api.example.com/api/login",
                status=500,
                request_headers={"Authorization": "Bearer secret-token", "Cookie": "sid=abc"},
                body='{"token":"abc","password":"pw","secret":"hidden"}',
            ),
        ]}}

        res = self.run_script(har)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        raw = res.stdout
        self.assertNotIn("secret-token", raw)
        self.assertNotIn("sid=abc", raw)
        self.assertNotIn('"pw"', raw)
        self.assertIn("<redacted>", raw)


if __name__ == "__main__":
    unittest.main()
