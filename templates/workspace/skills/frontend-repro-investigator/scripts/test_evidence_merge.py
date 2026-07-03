#!/usr/bin/env python3
from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("evidence_merge.py")


class EvidenceMergeTest(unittest.TestCase):
    def run_script(self, payloads):
        paths = []
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        for name, payload in payloads.items():
            path = Path(tmp.name) / name
            path.write_text(json.dumps(payload), encoding="utf-8")
            paths.append(str(path))
        return subprocess.run(
            [sys.executable, str(SCRIPT), *paths],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )

    def test_merges_trace_ids_endpoints_and_findings_with_source_priority(self):
        res = self.run_script({
            "console.json": {
                "frontend_findings": [{"type": "console_api_failure"}],
                "backend_handoff": {
                    "trace_ids": ["req-1"],
                    "candidate_endpoints": ["/api/profile"],
                },
                "redacted_input_preview": "{}",
            },
            "sentry.json": {
                "frontend_findings": [{"type": "sentry_event"}],
                "backend_handoff": {
                    "trace_ids": ["trace-1", "req-1"],
                    "candidate_endpoints": ["/api/profile", "/api/orders/42"],
                },
                "redacted_event_preview": "{}",
            },
            "har.json": {
                "frontend_findings": [{"type": "slow_api"}],
                "backend_handoff": {
                    "trace_ids": [],
                    "candidate_endpoints": ["/graphql"],
                },
                "failed_requests": [],
            },
        })

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertEqual(payload["backend_handoff"]["trace_ids"], ["req-1", "trace-1"])
        self.assertEqual(payload["summary"]["source_count"], 3)
        self.assertEqual(payload["summary"]["frontend_finding_count"], 3)
        endpoints = payload["backend_handoff"]["candidate_endpoints"]
        self.assertEqual(endpoints[0], "/api/profile")
        self.assertIn("/api/orders/42", endpoints)
        self.assertIn("/graphql", endpoints)
        profile_source = [
            item for item in payload["backend_handoff"]["endpoint_sources"]
            if item["endpoint"] == "/api/profile"
        ][0]
        self.assertEqual(profile_source["source"], "sentry")

    def test_invalid_json_returns_json_error(self):
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8") as f:
            f.write("{bad")
            path = f.name

        res = subprocess.run(
            [sys.executable, str(SCRIPT), path],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )

        self.assertEqual(res.returncode, 2)
        self.assertEqual(json.loads(res.stdout)["error"]["code"], 2)
        self.assertEqual(res.stderr, "")


if __name__ == "__main__":
    unittest.main()
