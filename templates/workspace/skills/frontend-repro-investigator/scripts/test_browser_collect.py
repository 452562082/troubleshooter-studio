#!/usr/bin/env python3
import json
import os
import shutil
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


SCRIPT = Path(__file__).with_name("browser_collect.mjs")


class SmokePage(BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"""<!doctype html>
<html>
  <body>
    <script>
      console.log("tshoot smoke console");
      fetch("/api/smoke").catch(() => {});
    </script>
  </body>
</html>
"""
        if self.path == "/api/smoke":
            body = b'{"ok":true}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
        else:
            self.send_response(200)
            self.send_header("Content-Type", "text/html")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return


class BrowserCollectTest(unittest.TestCase):
    def run_script(self, *args, script=SCRIPT, env=None):
        return subprocess.run(
            ["node", str(Path(script).resolve()), *args],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
            env=env,
        )

    def test_plan_outputs_artifact_paths_without_browser(self):
        url = "https://shop.example.com/orders/42"

        res = self.run_script("--url", url, "--out", "/tmp/tshoot-artifacts", "--plan")

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertEqual(payload["mode"], "plan")
        self.assertEqual(payload["url"], url)
        self.assertTrue(payload["artifacts"]["har"].endswith("network.har"))
        self.assertTrue(payload["artifacts"]["console"].endswith("console.jsonl"))

    def test_missing_url_returns_json_error(self):
        res = self.run_script("--plan")

        self.assertNotEqual(res.returncode, 0)
        payload = json.loads(res.stdout)
        self.assertIn("error", payload)

    def test_missing_playwright_from_temp_script_returns_install_hint(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            script = tmp_path / "browser_collect.mjs"
            shutil.copy2(SCRIPT, script)

            res = self.run_script(
                "--url",
                "https://shop.example.com/orders/42",
                "--out",
                str(tmp_path / "artifacts"),
                script=script,
            )

            self.assertEqual(res.returncode, 3, res.stderr + res.stdout)
            self.assertEqual(res.stderr, "")
            payload = json.loads(res.stdout)
            self.assertEqual(payload["mode"], "error")
            self.assertEqual(payload["code"], 3)
            self.assertIn("install_hint", payload)
            self.assertTrue(payload["artifacts"]["har"].endswith("network.har"))

    def test_broken_playwright_import_returns_json_without_stack(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            script = tmp_path / "browser_collect.mjs"
            shutil.copy2(SCRIPT, script)
            module_dir = tmp_path / "node_modules" / "playwright"
            module_dir.mkdir(parents=True)
            (module_dir / "package.json").write_text(
                json.dumps({"name": "playwright", "main": "index.js"}),
                encoding="utf-8",
            )
            (module_dir / "index.js").write_text(
                "throw new Error('fake import exploded');\n",
                encoding="utf-8",
            )

            res = self.run_script(
                "--url",
                "https://shop.example.com/orders/42",
                "--out",
                str(tmp_path / "artifacts"),
                script=script,
            )

            self.assertEqual(res.returncode, 4, res.stderr + res.stdout)
            self.assertEqual(res.stderr, "")
            payload = json.loads(res.stdout)
            self.assertEqual(payload["mode"], "error")
            self.assertEqual(payload["code"], 4)
            self.assertIn("fake import exploded", payload["error"])

    def test_collect_with_fake_playwright_writes_artifacts_and_closes(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            script = tmp_path / "browser_collect.mjs"
            out_dir = tmp_path / "artifacts"
            marker = tmp_path / "marker.json"
            shutil.copy2(SCRIPT, script)
            module_dir = tmp_path / "node_modules" / "playwright"
            module_dir.mkdir(parents=True)
            (module_dir / "package.json").write_text(
                json.dumps({"name": "playwright", "main": "index.js"}),
                encoding="utf-8",
            )
            (module_dir / "index.js").write_text(
                """
const fs = require("node:fs");
const events = [];
function mark(name) {
  events.push(name);
  fs.writeFileSync(process.env.FAKE_PLAYWRIGHT_MARKER, JSON.stringify(events));
}

const fakePage = {
  handlers: {},
  on(name, handler) {
    this.handlers[name] = handler;
  },
  async goto() {
    if (this.handlers.console) {
      this.handlers.console({
        type: () => "log",
        text: () => "fake console message",
        location: () => ({ url: "fake.js", lineNumber: 1, columnNumber: 2 }),
      });
    }
  },
  async screenshot(opts) {
    mark("screenshot");
    fs.writeFileSync(opts.path, "fake screenshot");
  },
};

const fakeContext = {
  tracing: {
    async start() {
      mark("tracing.start");
    },
    async stop(opts) {
      mark("tracing.stop");
      fs.writeFileSync(opts.path, "fake trace");
    },
  },
  async newPage() {
    return fakePage;
  },
  async close() {
    mark("context.close");
  },
};

const fakeBrowser = {
  async newContext(opts) {
    fs.writeFileSync(opts.recordHar.path, "fake har");
    return fakeContext;
  },
  async close() {
    mark("browser.close");
  },
};

module.exports = {
  chromium: {
    async launch() {
      mark("chromium.launch");
      return fakeBrowser;
    },
  },
};
""",
                encoding="utf-8",
            )
            env = {**os.environ, "FAKE_PLAYWRIGHT_MARKER": str(marker)}

            res = self.run_script(
                "--url",
                "https://shop.example.com/orders/42",
                "--out",
                str(out_dir),
                script=script,
                env=env,
            )

            self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
            payload = json.loads(res.stdout)
            self.assertEqual(payload["mode"], "collect")
            self.assertTrue(Path(payload["artifacts"]["har"]).exists())
            self.assertTrue(Path(payload["artifacts"]["console"]).exists())
            self.assertTrue(Path(payload["artifacts"]["screenshot"]).exists())
            self.assertTrue(Path(payload["artifacts"]["trace"]).exists())
            self.assertIn(
                "fake console message",
                Path(payload["artifacts"]["console"]).read_text(encoding="utf-8"),
            )
            self.assertEqual(
                json.loads(marker.read_text(encoding="utf-8")),
                [
                    "chromium.launch",
                    "tracing.start",
                    "screenshot",
                    "tracing.stop",
                    "context.close",
                    "browser.close",
                ],
            )

    @unittest.skipUnless(
        os.environ.get("TSHOOT_BROWSER_COLLECT_SMOKE") == "1",
        "set TSHOOT_BROWSER_COLLECT_SMOKE=1 to run real Playwright smoke",
    )
    def test_real_playwright_smoke_collects_local_page(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), SmokePage)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.shutdown)
        self.addCleanup(lambda: thread.join(timeout=5))
        self.addCleanup(server.server_close)

        with tempfile.TemporaryDirectory() as tmp:
            out_dir = Path(tmp) / "artifacts"
            res = self.run_script(
                "--url",
                f"http://127.0.0.1:{server.server_port}/",
                "--out",
                str(out_dir),
            )
            if res.returncode == 3:
                self.skipTest("Playwright is not installed")

            self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
            payload = json.loads(res.stdout)
            self.assertTrue(Path(payload["artifacts"]["har"]).exists())
            self.assertTrue(Path(payload["artifacts"]["console"]).exists())
            self.assertTrue(Path(payload["artifacts"]["screenshot"]).exists())
            self.assertTrue(Path(payload["artifacts"]["trace"]).exists())
            self.assertIn(
                "tshoot smoke console",
                Path(payload["artifacts"]["console"]).read_text(encoding="utf-8"),
            )


if __name__ == "__main__":
    unittest.main()
