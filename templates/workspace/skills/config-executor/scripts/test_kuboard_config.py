#!/usr/bin/env python3
"""
Repo-side tests for kuboard_config.py.

These tests are NOT shipped to bot workspaces (generator filters test_*.py).

Run:
  python3 templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
"""

import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


SCRIPT = Path(__file__).with_name("kuboard_config.py")


class MockKuboard(BaseHTTPRequestHandler):
    calls: list[str] = []
    mode = "v4"

    def log_message(self, fmt, *args):  # keep test output clean
        return

    def do_GET(self):
        MockKuboard.calls.append(self.path)
        parsed = urllib.parse.urlparse(self.path)
        qs = urllib.parse.parse_qs(parsed.query)
        if parsed.path.endswith("/cluster-namespace-tree"):
            if MockKuboard.mode == "v3":
                self._json({"error": "v4 endpoint not found"}, status=404)
                return
            if MockKuboard.mode == "v3-forbidden-tree":
                self._json({"error": "v4 endpoint forbidden"}, status=403)
                return
            if MockKuboard.mode == "v3-html-tree":
                self.send_response(200)
                raw = b"<html>cloud gateway</html>"
                self.send_header("Content-Type", "text/html")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)
                return
            self._json({
                "data": {
                    "treeItems": [
                        {"name": "dev-cluster", "id": "cluster-uid-1"},
                    ],
                },
            })
            return
        if parsed.path.endswith("/cluster-cache/direct"):
            if qs.get("clusterId") != ["cluster-uid-1"]:
                self._json({"error": "bad cluster"}, status=400)
                return
            if qs.get("resource") != ["configmaps"] or qs.get("namespace") != ["default"]:
                self._json({"error": "bad query"}, status=400)
                return
            self._json({
                "data": {
                    "list": [
                        {
                            "data": {
                                "metadata": {"name": "app-config"},
                                "data": {"DB_HOST": "db.internal", "DB_PORT": "3306", "REDIS_HOST": "redis.internal", "REDIS_PORT": "6379"},
                            },
                        },
                    ],
                },
            })
            return
        if parsed.path.endswith("/k8s-api/dev-cluster/api/v1/namespaces/default/configmaps/app-config"):
            cookie = self.headers.get("Cookie", "")
            if "KuboardUsername=admin" not in cookie or "KuboardAccessKey=key.id" not in cookie:
                self._json({"error": "bad cookie", "cookie": cookie}, status=403)
                return
            self._json({
                "kind": "ConfigMap",
                "data": {"DB_HOST": "v3-db.internal", "REDIS_PORT": "6380"},
            })
            return
        self._json({"error": "not found", "path": self.path}, status=404)

    def _json(self, body, status=200):
        raw = json.dumps(body).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


class KuboardConfigTest(unittest.TestCase):
    def setUp(self):
        MockKuboard.calls = []
        MockKuboard.mode = "v4"
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), MockKuboard)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.base_url = f"http://127.0.0.1:{self.server.server_port}"
        self.tmp = tempfile.TemporaryDirectory()
        self.home = Path(self.tmp.name)
        (self.home / ".openclaw").mkdir()

    def tearDown(self):
        self.server.shutdown()
        self.thread.join(timeout=2)
        self.server.server_close()
        self.tmp.cleanup()

    def run_script(self, *args):
        env = os.environ.copy()
        env["HOME"] = str(self.home)
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=env,
            check=False,
        )

    def test_get_reads_kuboard_configmap_data_from_openclaw_creds(self):
        creds = {
            "kuboard": {
                "default": {
                    "dev": {
                        "url": self.base_url,
                        "access_key": "ak",
                    },
                },
            },
        }
        (self.home / ".openclaw" / "shop-creds.json").write_text(
            json.dumps(creds), encoding="utf-8"
        )

        res = self.run_script(
            "get",
            "--agent-id", "shop",
            "--env", "dev",
            "--cluster", "dev-cluster",
            "--namespace", "default",
            "--configmap", "app-config",
        )

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertEqual(payload["format"], "k8s-env-flat")
        self.assertEqual(payload["cluster"], "dev-cluster")
        self.assertEqual(payload["namespace"], "default")
        self.assertEqual(payload["configmap"], "app-config")
        self.assertEqual(payload["data"]["DB_HOST"], "db.internal")
        self.assertEqual(json.loads(payload["content"])["REDIS_PORT"], "6379")
        self.assertTrue(payload["runtime"]["redis"]["resolved"])
        self.assertEqual(payload["runtime"]["redis"]["host"], "redis.internal")
        self.assertTrue(payload["runtime"]["mysql"]["resolved"])
        self.assertEqual(payload["runtime"]["mysql"]["host"], "db.internal")
        self.assertTrue(any("cluster-namespace-tree" in p for p in MockKuboard.calls))
        self.assertTrue(any("cluster-cache/direct" in p for p in MockKuboard.calls))

    def test_missing_env_credentials_returns_json_error(self):
        (self.home / ".openclaw" / "shop-creds.json").write_text(
            json.dumps({"kuboard": {"default": {}}}), encoding="utf-8"
        )

        res = self.run_script(
            "get",
            "--agent-id", "shop",
            "--env", "prod",
            "--cluster", "dev-cluster",
            "--namespace", "default",
            "--configmap", "app-config",
        )

        self.assertEqual(res.returncode, 2)
        payload = json.loads(res.stdout)
        self.assertIn("error", payload)
        self.assertIn("hint", payload)
        self.assertIn("prod", payload["hint"])

    def test_get_reads_kuboard_v3_configmap_data(self):
        MockKuboard.mode = "v3"
        self.assert_kuboard_v3_configmap_fallback()

    def test_get_falls_back_to_kuboard_v3_when_v4_tree_is_forbidden(self):
        MockKuboard.mode = "v3-forbidden-tree"
        self.assert_kuboard_v3_configmap_fallback()

    def test_get_falls_back_to_kuboard_v3_when_v4_tree_is_not_json(self):
        MockKuboard.mode = "v3-html-tree"
        self.assert_kuboard_v3_configmap_fallback()

    def assert_kuboard_v3_configmap_fallback(self):
        creds = {
            "kuboard": {
                "default": {
                    "dev": {
                        "url": self.base_url,
                        "access_key": "key.id",
                        "username": "admin",
                    },
                },
            },
        }
        (self.home / ".openclaw" / "shop-creds.json").write_text(
            json.dumps(creds), encoding="utf-8"
        )

        res = self.run_script(
            "get",
            "--agent-id", "shop",
            "--env", "dev",
            "--cluster", "dev-cluster",
            "--namespace", "default",
            "--configmap", "app-config",
        )

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertEqual(payload["format"], "k8s-env-flat")
        self.assertEqual(payload["data"]["DB_HOST"], "v3-db.internal")
        self.assertEqual(json.loads(payload["content"])["REDIS_PORT"], "6380")
        self.assertTrue(any("/k8s-api/dev-cluster/api/v1/namespaces/default/configmaps/app-config" in p for p in MockKuboard.calls))


if __name__ == "__main__":
    unittest.main()
