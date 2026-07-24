# Remaining Risk Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the remaining troubleshooting quality gaps so generated bots can turn frontend reproduction evidence into precise backend/data-layer investigation steps, with CI and tests guarding the behavior.

**Architecture:** Keep the current artifact-first approach. Extend existing repo-side scanners and generated workspace scripts instead of introducing a new runtime service; generated skills consume structured local evidence files and routing references. Template helpers should expose parsed analysis facts to templates, while Python analyzers keep browser evidence parsing deterministic and testable.

**Tech Stack:** Go generator/analyzer tests, Go templates, Python standard-library scripts, GitHub Actions, GitLab CI, Makefile.

---

## Files To Modify

- Modify `templates/workspace/skills/config-executor/scripts/kuboard_config.py`: extend `resolve_runtime` to emit postgres, clickhouse, kafka, rabbitmq runtime blocks.
- Modify `templates/workspace/skills/config-executor/scripts/test_kuboard_config.py`: add assertions for the new runtime blocks and URI/URL fallback shapes.
- Modify `internal/generator/generator.go`: add `FrontendEndpointsByRepo map[string][]string` to `Context` and populate it from analyzer notes.
- Modify `internal/generator/funcs.go`: add a template helper `frontendEndpointsForRepo`.
- Modify `templates/workspace/skills/routing/references/frontend-entry-map.yaml.tmpl`: render concrete frontend endpoint paths and path-to-candidate-service hints.
- Modify `internal/generator/generator_test.go`: verify generated `frontend-entry-map.yaml` includes endpoints from loaded analysis.
- Modify `templates/workspace/skills/frontend-repro-investigator/scripts/har_analyzer.py`: add CORS/preflight, network abort, redirect, GraphQL, traceparent extraction, and stronger redaction.
- Modify `templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py`: add regression tests for those cases.
- Create `templates/workspace/skills/frontend-repro-investigator/scripts/console_analyzer.py`: parse browser console/Sentry-like JSON/text into structured frontend and backend handoff evidence.
- Create `templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py`: test JS stack/API URL extraction and sensitive value redaction.
- Modify `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`: document the console analyzer command and handoff fields.
- Modify `CONTRIBUTING.md`: add the new repo-side script tests.
- Modify `Makefile`: align local `lint`/`audit` with CI behavior.
- Modify `.github/workflows/ci.yml`: run on `test` branch and use tracked Go files for gofmt.
- Modify `.gitlab-ci.yml`: run on `test` branch as well as `main` and MR.
- Modify `scripts/check-go-coverage.sh` only if touched packages lower package coverage unexpectedly; keep existing global gate behavior intact.
- Add focused Go tests in existing package test files when new generator/analyzer code changes require coverage.

---

## Task 1: Expand Kuboard Runtime Resolution

**Files:**
- Modify `templates/workspace/skills/config-executor/scripts/kuboard_config.py`
- Modify `templates/workspace/skills/config-executor/scripts/test_kuboard_config.py`

- [ ] **Step 1: Write the failing runtime coverage test**

Add a test to `templates/workspace/skills/config-executor/scripts/test_kuboard_config.py`:

```python
    def test_runtime_resolution_covers_data_and_messaging_stores(self):
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

        original_do_get = MockKuboard.do_GET

        def do_get_with_rich_config(handler):
            parsed = urllib.parse.urlparse(handler.path)
            if parsed.path.endswith("/cluster-cache/direct"):
                handler._json({
                    "data": {
                        "list": [
                            {
                                "data": {
                                    "metadata": {"name": "app-config"},
                                    "data": {
                                        "POSTGRES_HOST": "pg.internal",
                                        "POSTGRES_PORT": "5433",
                                        "POSTGRES_DB": "orders",
                                        "POSTGRES_USER": "order_user",
                                        "CLICKHOUSE_URL": "http://ch.internal:8123",
                                        "KAFKA_BOOTSTRAP_SERVERS": "kafka-1:9092,kafka-2:9092",
                                        "RABBITMQ_HOST": "mq.internal",
                                        "RABBITMQ_PORT": "5673",
                                        "RABBITMQ_VHOST": "/orders",
                                    },
                                },
                            },
                        ],
                    },
                })
                return
            original_do_get(handler)

        MockKuboard.do_GET = do_get_with_rich_config
        self.addCleanup(lambda: setattr(MockKuboard, "do_GET", original_do_get))

        res = self.run_script(
            "get",
            "--agent-id", "shop",
            "--env", "dev",
            "--cluster", "dev-cluster",
            "--namespace", "default",
            "--configmap", "app-config",
        )

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        runtime = json.loads(res.stdout)["runtime"]
        self.assertEqual(runtime["postgres"]["host"], "pg.internal")
        self.assertEqual(runtime["postgres"]["port"], 5433)
        self.assertEqual(runtime["postgres"]["database"], "orders")
        self.assertEqual(runtime["postgres"]["user"], "order_user")
        self.assertEqual(runtime["clickhouse"]["hosts"], ["http://ch.internal:8123"])
        self.assertEqual(runtime["kafka"]["bootstrap_servers"], ["kafka-1:9092", "kafka-2:9092"])
        self.assertEqual(runtime["rabbitmq"]["host"], "mq.internal")
        self.assertEqual(runtime["rabbitmq"]["port"], 5673)
        self.assertEqual(runtime["rabbitmq"]["vhost"], "/orders")
```

- [ ] **Step 2: Run the targeted test and confirm it fails**

Run:

```bash
python3 templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
```

Expected: FAIL because `runtime["postgres"]`, `runtime["clickhouse"]`, `runtime["kafka"]`, and `runtime["rabbitmq"]` do not exist.

- [ ] **Step 3: Implement runtime parsing helpers**

In `templates/workspace/skills/config-executor/scripts/kuboard_config.py`, add this helper near `int_or_none`:

```python
def csv_values(value: str) -> list[str]:
    return [item.strip() for item in str(value).split(",") if item.strip()]
```

Replace `resolve_runtime` with:

```python
def resolve_runtime(data: dict[str, str]) -> dict:
    redis_host = first_value(data, "REDIS_HOST", "SPRING_REDIS_HOST")
    redis_port = first_value(data, "REDIS_PORT", "SPRING_REDIS_PORT")
    mysql_host = first_value(data, "MYSQL_HOST", "DB_HOST", "DATABASE_HOST")
    mysql_port = first_value(data, "MYSQL_PORT", "DB_PORT", "DATABASE_PORT")
    mysql_db = first_value(data, "MYSQL_DATABASE", "MYSQL_DB", "DB_DATABASE", "DATABASE_NAME")
    mysql_user = first_value(data, "MYSQL_USER", "DB_USERNAME", "DB_USER", "DATABASE_USER")
    postgres_host = first_value(data, "POSTGRES_HOST", "POSTGRESQL_HOST", "PG_HOST", "SPRING_DATASOURCE_HOST")
    postgres_port = first_value(data, "POSTGRES_PORT", "POSTGRESQL_PORT", "PG_PORT")
    postgres_db = first_value(data, "POSTGRES_DATABASE", "POSTGRES_DB", "POSTGRESQL_DATABASE", "POSTGRESQL_DB", "PGDATABASE")
    postgres_user = first_value(data, "POSTGRES_USER", "POSTGRESQL_USER", "PGUSER")
    mongo_uri = first_value(data, "MONGO_URI", "MONGODB_URI", "SPRING_DATA_MONGODB_URI")
    es_url = first_value(data, "ELASTICSEARCH_URL", "ES_URL", "ELASTICSEARCH_HOSTS", "ES_HOSTS")
    clickhouse_url = first_value(data, "CLICKHOUSE_URL", "CLICKHOUSE_HOSTS", "CH_URL", "CH_HOSTS")
    clickhouse_host = first_value(data, "CLICKHOUSE_HOST", "CH_HOST")
    clickhouse_port = first_value(data, "CLICKHOUSE_PORT", "CH_PORT")
    kafka_servers = first_value(data, "KAFKA_BOOTSTRAP_SERVERS", "KAFKA_BROKERS", "SPRING_KAFKA_BOOTSTRAP_SERVERS")
    rabbitmq_host = first_value(data, "RABBITMQ_HOST", "SPRING_RABBITMQ_HOST")
    rabbitmq_port = first_value(data, "RABBITMQ_PORT", "SPRING_RABBITMQ_PORT")
    rabbitmq_vhost = first_value(data, "RABBITMQ_VHOST", "SPRING_RABBITMQ_VIRTUAL_HOST")
    return {
        "redis": {"host": redis_host, "port": int_or_none(redis_port), "resolved": bool(redis_host)},
        "mysql": {
            "host": mysql_host,
            "port": int_or_none(mysql_port) or 3306,
            "database": mysql_db,
            "user": mysql_user,
            "resolved": bool(mysql_host),
        },
        "postgres": {
            "host": postgres_host,
            "port": int_or_none(postgres_port) or 5432,
            "database": postgres_db,
            "user": postgres_user,
            "resolved": bool(postgres_host),
        },
        "mongo": {"uri": mongo_uri, "resolved": bool(mongo_uri)},
        "elasticsearch": {"hosts": csv_values(es_url) if "," in es_url else ([es_url] if es_url else []), "resolved": bool(es_url)},
        "clickhouse": {
            "hosts": csv_values(clickhouse_url) if clickhouse_url else ([f"{clickhouse_host}:{int_or_none(clickhouse_port) or 8123}"] if clickhouse_host else []),
            "resolved": bool(clickhouse_url or clickhouse_host),
        },
        "kafka": {"bootstrap_servers": csv_values(kafka_servers), "resolved": bool(kafka_servers)},
        "rabbitmq": {
            "host": rabbitmq_host,
            "port": int_or_none(rabbitmq_port) or 5672,
            "vhost": rabbitmq_vhost,
            "resolved": bool(rabbitmq_host),
        },
    }
```

- [ ] **Step 4: Run Kuboard tests**

Run:

```bash
python3 templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
```

Expected: `Ran 6 tests` and `OK`.

---

## Task 2: Render Frontend Endpoints Into Routing Map

**Files:**
- Modify `internal/generator/generator.go`
- Modify `internal/generator/funcs.go`
- Modify `templates/workspace/skills/routing/references/frontend-entry-map.yaml.tmpl`
- Modify `internal/generator/generator_test.go`

- [ ] **Step 1: Write the failing generator test**

Add this test to `internal/generator/generator_test.go`:

```go
func TestGenerate_FrontendEntryMapIncludesAnalysisEndpoints(t *testing.T) {
	cfg := loadCfg(t, "examples/three-tier-troubleshooter.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	g := New(cfg, tr, out)
	g.LoadAnalysisReport(analyzer.Report{
		Repos: []analyzer.RepoAnalysis{
			{
				Name: "shop-web",
				Notes: []string{
					"api_endpoint[src/api.ts]=/api/orders",
					"api_endpoint[src/pay.ts]=/api/payments/submit",
					"api_endpoint[src/ignored.ts]=/healthz",
				},
			},
		},
	})
	if err := g.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	fm := readFile(t, filepath.Join(out, "templates/workspace-template/skills/routing/references/frontend-entry-map.yaml"))
	for _, want := range []string{
		`endpoint_paths:`,
		`- "/api/orders"`,
		`- "/api/payments/submit"`,
		`path_candidates:`,
	} {
		if !strings.Contains(fm, want) {
			t.Fatalf("frontend-entry-map missing %q:\n%s", want, fm)
		}
	}
	if strings.Contains(fm, "/healthz") {
		t.Fatalf("frontend-entry-map should not include non-api endpoint:\n%s", fm)
	}
}
```

Ensure `internal/generator/generator_test.go` imports `github.com/xiaolong/troubleshooter-studio/internal/analyzer` if it is not already imported.

- [ ] **Step 2: Run the targeted generator test and confirm it fails**

Run:

```bash
go test ./internal/generator/ -run TestGenerate_FrontendEntryMapIncludesAnalysisEndpoints
```

Expected: FAIL because no endpoint paths are rendered.

- [ ] **Step 3: Store endpoint notes in generator context**

In `internal/generator/generator.go`, add a field to `Context`:

```go
	// FrontendEndpointsByRepo stores api_endpoint[...] notes extracted by node analyzer.
	FrontendEndpointsByRepo map[string][]string
```

Initialize it in `New`:

```go
FrontendEndpointsByRepo: map[string][]string{},
```

Add this helper near `LoadAnalysisReport`:

```go
func frontendEndpointNotes(notes []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, note := range notes {
		const marker = "]="
		if !strings.HasPrefix(note, "api_endpoint[") || !strings.Contains(note, marker) {
			continue
		}
		path := strings.TrimSpace(note[strings.Index(note, marker)+len(marker):])
		if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "http") {
			continue
		}
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}
```

Add imports to `internal/generator/generator.go`:

```go
	"sort"
	"strings"
```

Inside `LoadAnalysisReport`, in the `if ra.Name != ""` block, add:

```go
			if endpoints := frontendEndpointNotes(ra.Notes); len(endpoints) > 0 {
				g.Ctx.FrontendEndpointsByRepo[ra.Name] = endpoints
			}
```

- [ ] **Step 4: Add template helper**

In `internal/generator/funcs.go`, add this entry to `funcMap`:

```go
		"frontendEndpointsForRepo": func(ctx *Context, repoName string) []string {
			return ctx.FrontendEndpointsByRepo[repoName]
		},
```

- [ ] **Step 5: Render endpoint paths and path candidates**

In `templates/workspace/skills/routing/references/frontend-entry-map.yaml.tmpl`, after `api_domains`, render:

```gotemplate
    endpoint_paths:
    {{- range frontendEndpointsForRepo $ctx $repo.Name}}
      - {{. | printf "%q"}}
    {{- else}}
      []
    {{- end}}
    path_candidates:
    {{- range frontendEndpointsForRepo $ctx $repo.Name}}
      {{. | printf "%q"}}:
        candidate_services:
        {{- range $ctx.Repos}}
          {{- if and (.RequiresServiceNames) (ne .Name $repo.Name)}}
            {{- range .ServiceNames}}
          - {{. | printf "%q"}}
            {{- end}}
          {{- end}}
        {{- end}}
        hint: "Match this endpoint with gateway routes, access logs, or trace spans before selecting the backend service."
    {{- else}}
      {}
    {{- end}}
```

- [ ] **Step 6: Run generator tests**

Run:

```bash
go test ./internal/generator/ -run 'TestGenerate_FrontendEntryMap|TestGenerate_FrontendEntryMapIncludesAnalysisEndpoints'
```

Expected: PASS.

---

## Task 3: Harden HAR Evidence Analysis

**Files:**
- Modify `templates/workspace/skills/frontend-repro-investigator/scripts/har_analyzer.py`
- Modify `templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py`

- [ ] **Step 1: Add failing HAR regression tests**

Append tests to `templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py`:

```python
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

    def test_redacts_sensitive_headers_and_body_values(self):
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
```

- [ ] **Step 2: Run HAR tests and confirm failures**

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py
```

Expected: FAIL for missing finding types and traceparent normalization.

- [ ] **Step 3: Implement HAR enhancements**

In `har_analyzer.py`, add helpers:

```python
SENSITIVE_HEADER_NAMES = {"authorization", "cookie", "set-cookie", "x-api-key"}


def redact_text(text: str) -> str:
    text = re.sub(r"(?i)(token|password|secret|authorization|cookie)([\"']?\s*[:=]\s*[\"']?)[^,\"'&\s}]+", r"\1\2<redacted>", str(text))
    text = re.sub(r"(?i)(bearer\s+)[a-z0-9._~+/=-]+", r"\1<redacted>", text)
    return text


def normalized_trace_values(values: list[str]) -> list[str]:
    out = []
    for value in values:
        if value.startswith("00-") and value.count("-") >= 3:
            parts = value.split("-")
            if len(parts) >= 4 and len(parts[1]) == 32:
                out.append(parts[1])
                continue
        out.append(value)
    return out


def response_header(entry: dict, name: str) -> str:
    target = name.lower()
    for h in ((entry.get("response") or {}).get("headers") or []):
        if str(h.get("name", "")).lower() == target:
            return str(h.get("value", "")).strip()
    return ""
```

Update `body_snippet`:

```python
def body_snippet(entry: dict) -> str:
    text = (((entry.get("response") or {}).get("content") or {}).get("text") or "")
    return redact_text(text)[:500]
```

Update `summarize_entry` so trace IDs are normalized and sensitive request headers are not copied:

```python
        "trace_ids": sorted(set(normalized_trace_values(headers))),
```

Update `analyze` loop with these checks:

```python
        if status == 0:
            failed.append(item)
            frontend_findings.append({
                "type": "network_request_aborted",
                "url": url,
                "status": status,
                "hint": "Browser saw status 0. Check CORS, DNS/TLS, ad blockers, gateway reset, or client-side aborts.",
            })
            candidate_endpoints.append(item["path"])
            continue
        if req_method := item["method"].upper():
            if req_method == "OPTIONS" and status >= 400:
                frontend_findings.append({
                    "type": "cors_preflight_failed",
                    "url": url,
                    "status": status,
                    "hint": "Check gateway CORS policy, allowed origin, allowed headers, and credentials mode.",
                })
        location = response_header(entry, "Location")
        if 300 <= status < 400 and location and re.search(r"(?i)(login|sso|oauth|auth)", location):
            frontend_findings.append({
                "type": "auth_redirect",
                "url": url,
                "status": status,
                "location": location,
                "hint": "Check frontend auth state, session expiry, gateway auth middleware, and environment domain config.",
            })
            candidate_endpoints.append(item["path"])
        if item["path"].endswith("/graphql") and '"errors"' in item["response_snippet"]:
            frontend_findings.append({
                "type": "graphql_error_response",
                "url": url,
                "status": status,
                "hint": "HTTP 200 contains GraphQL errors. Inspect resolver error, operation name, and backend trace/logs.",
            })
            candidate_endpoints.append(item["path"])
```

- [ ] **Step 4: Run HAR tests**

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py
```

Expected: all tests pass.

---

## Task 4: Add Console/Sentry Evidence Analyzer

**Files:**
- Create `templates/workspace/skills/frontend-repro-investigator/scripts/console_analyzer.py`
- Create `templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py`
- Modify `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`
- Modify `CONTRIBUTING.md`

- [ ] **Step 1: Create failing console analyzer tests**

Create `templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py`:

```python
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
            f.write(content)
            path = f.name
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--file", path],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )

    def test_extracts_api_url_js_stack_and_trace_id(self):
        content = """
        TypeError: Failed to fetch
            at submitOrder (https://shop.example.com/assets/order.js:10:2)
        POST https://api.example.com/api/orders/42 500
        x-request-id: req-123
        """

        res = self.run_script(content)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertIn("/api/orders/42", payload["backend_handoff"]["candidate_endpoints"])
        self.assertIn("req-123", payload["backend_handoff"]["trace_ids"])
        self.assertEqual(payload["frontend_findings"][0]["type"], "console_api_failure")

    def test_parses_sentry_like_json_and_redacts_sensitive_values(self):
        content = json.dumps({
            "message": "Request failed with status code 401",
            "request": {"url": "https://api.example.com/api/profile?token=secret"},
            "contexts": {"trace": {"trace_id": "trace-xyz"}},
            "extra": {"Authorization": "Bearer hidden"},
        })

        res = self.run_script(content)

        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        raw = res.stdout
        self.assertIn("/api/profile", raw)
        self.assertIn("trace-xyz", raw)
        self.assertNotIn("secret", raw)
        self.assertNotIn("hidden", raw)


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run the new test and confirm it fails because the script is missing**

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py
```

Expected: FAIL with no `console_analyzer.py`.

- [ ] **Step 3: Create console analyzer script**

Create `templates/workspace/skills/frontend-repro-investigator/scripts/console_analyzer.py`:

```python
#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import re
import sys
from urllib.parse import urlparse


URL_RE = re.compile(r"https?://[^\s\"')]+")
TRACE_RE = re.compile(r"(?i)\b(?:x-request-id|request-id|x-trace-id|trace-id)\s*[:=]\s*([a-z0-9._:-]+)")
STACK_RE = re.compile(r"\bat\s+([^\n]+)")


def redact(text: str) -> str:
    text = re.sub(r"(?i)(token|password|secret|authorization|cookie)([\"']?\s*[:=]\s*[\"']?)[^,\"'&\s}]+", r"\1\2<redacted>", str(text))
    text = re.sub(r"(?i)(bearer\s+)[a-z0-9._~+/=-]+", r"\1<redacted>", text)
    text = re.sub(r"(?i)([?&](?:token|password|secret)=)[^&\s]+", r"\1<redacted>", text)
    return text


def path_for(url: str) -> str:
    return urlparse(url).path or "/"


def walk_json(value):
    if isinstance(value, dict):
        for k, v in value.items():
            yield str(k), v
            yield from walk_json(v)
    elif isinstance(value, list):
        for item in value:
            yield from walk_json(item)


def analyze_text(raw: str) -> dict:
    clean = redact(raw)
    urls = URL_RE.findall(clean)
    endpoints = sorted({path_for(u) for u in urls if path_for(u).startswith(("/api/", "/graphql"))})
    trace_ids = sorted(set(TRACE_RE.findall(clean)))
    stack_frames = [m.group(1).strip() for m in STACK_RE.finditer(clean)][:10]
    findings = []
    if endpoints:
        findings.append({
            "type": "console_api_failure",
            "candidate_endpoints": endpoints,
            "hint": "Use the endpoint path with HAR time window, gateway logs, or trace IDs to select backend service.",
        })
    if stack_frames:
        findings.append({
            "type": "javascript_stack",
            "frames": stack_frames,
            "hint": "Use source map or repo-path-map to locate the request wrapper/component.",
        })
    return {
        "summary": {
            "candidate_endpoint_count": len(endpoints),
            "trace_id_count": len(trace_ids),
            "stack_frame_count": len(stack_frames),
        },
        "frontend_findings": findings,
        "backend_handoff": {
            "trace_ids": trace_ids,
            "candidate_endpoints": endpoints,
        },
        "redacted_input_preview": clean[:1000],
    }


def analyze_json(raw: str) -> dict:
    doc = json.loads(raw)
    text = json.dumps(doc, ensure_ascii=False)
    result = analyze_text(text)
    trace_ids = set(result["backend_handoff"]["trace_ids"])
    for key, value in walk_json(doc):
        if key.lower() in {"trace_id", "traceid"} and value:
            trace_ids.add(str(value))
    result["backend_handoff"]["trace_ids"] = sorted(trace_ids)
    result["summary"]["trace_id_count"] = len(trace_ids)
    result["redacted_input_preview"] = redact(text)[:1000]
    return result


def analyze(raw: str) -> dict:
    stripped = raw.strip()
    if stripped.startswith(("{", "[")):
        try:
            return analyze_json(stripped)
        except json.JSONDecodeError:
            pass
    return analyze_text(raw)


def main() -> int:
    parser = argparse.ArgumentParser(description="Analyze browser console or Sentry-like evidence.")
    parser.add_argument("--file", help="Input file path. If omitted, read stdin.")
    args = parser.parse_args()
    raw = open(args.file, "r", encoding="utf-8").read() if args.file else sys.stdin.read()
    print(json.dumps(analyze(raw), ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

- [ ] **Step 4: Document console analyzer in the generated skill**

In `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`, add a command under evidence collection:

```markdown
Console / Sentry evidence:

```bash
python3 scripts/console_analyzer.py --file /path/to/console-or-sentry.txt
```

Use `backend_handoff.trace_ids` first. If absent, use `backend_handoff.candidate_endpoints` with `routing/references/frontend-entry-map.yaml`.
```

- [ ] **Step 5: Add repo-side test command to CONTRIBUTING**

Add to `CONTRIBUTING.md` testing requirements:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py
```

- [ ] **Step 6: Run console analyzer tests**

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py
```

Expected: `Ran 2 tests` and `OK`.

---

## Task 5: Align Local and Remote CI Guardrails

**Files:**
- Modify `Makefile`
- Modify `.github/workflows/ci.yml`
- Modify `.gitlab-ci.yml`

- [ ] **Step 1: Update GitHub Actions branch triggers**

In `.github/workflows/ci.yml`, change:

```yaml
on:
  push:
    branches: [main]
  pull_request:
```

to:

```yaml
on:
  push:
    branches: [main, test]
  pull_request:
```

- [ ] **Step 2: Align GitHub gofmt with tracked files**

In `.github/workflows/ci.yml`, replace the `gofmt -l` step body with:

```yaml
      - name: gofmt -l
        run: |
          out=$(git ls-files '*.go' | xargs gofmt -l)
          if [ -n "$out" ]; then
            echo "unformatted files detected:"
            echo "$out"
            echo ""
            echo "请在本地运行: gofmt -w <files>"
            exit 1
          fi
```

- [ ] **Step 3: Update GitLab workflow to include test branch pushes**

In `.gitlab-ci.yml`, add this rule after the main branch rule:

```yaml
    # test 分支 push → 跑,匹配当前开发分支的直接推送流程
    - if: '$CI_COMMIT_BRANCH == "test"'
```

- [ ] **Step 4: Align Makefile lint and audit**

In `Makefile`, replace `audit` and `lint` bodies with:

```makefile
.PHONY: audit
audit:
	cd $(WEB_SRC) && npm audit --audit-level=moderate
	@if command -v govulncheck >/dev/null 2>&1; then \
	  govulncheck ./...; \
	else \
	  echo "govulncheck not installed; install with: go install golang.org/x/vuln/cmd/govulncheck@v1.5.0"; \
	  exit 1; \
	fi

.PHONY: lint
lint:
	go vet ./...
	@out="$$(git ls-files '*.go' | xargs gofmt -l)"; \
	if [ -n "$$out" ]; then \
	  echo "gofmt 未通过:"; echo "$$out"; exit 1; \
	fi
	@echo "✓ go vet + gofmt clean"
	cd $(WEB_SRC) && npx vue-tsc --noEmit
```

- [ ] **Step 5: Validate workflow and Makefile syntax**

Run:

```bash
make lint
make audit
```

Expected: both commands pass. If `govulncheck` is missing locally, install it:

```bash
go install golang.org/x/vuln/cmd/govulncheck@v1.5.0
```

Then rerun `make audit`.

---

## Task 6: Focused Coverage and Regression Sweep

**Files:**
- Modify existing test files only where coverage or touched behavior requires it.
- Prefer `internal/generator/generator_test.go`, `internal/analyzer/node_analyzer_test.go`, and Python script tests over broad integration tests.

- [ ] **Step 1: Run package coverage after Tasks 1-5**

Run:

```bash
go test ./... -cover
```

Expected: all packages pass and total remains above the repository gate.

- [ ] **Step 2: If generator coverage drops, add endpoint-note helper test**

Add to an existing generator test file:

```go
func TestFrontendEndpointNotesFiltersAndSorts(t *testing.T) {
	got := frontendEndpointNotes([]string{
		"api_endpoint[src/a.ts]=/api/z",
		"api_endpoint[src/b.ts]=/healthz",
		"api_endpoint[src/c.ts]=/api/a",
		"api_endpoint[src/d.ts]=/api/a",
		"build_tool=vite",
	})
	want := []string{"/api/a", "/api/z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frontendEndpointNotes() = %#v, want %#v", got, want)
	}
}
```

Add import `reflect` if needed.

- [ ] **Step 3: Run coverage gate**

Run:

```bash
./scripts/check-go-coverage.sh
```

Expected: passes without changing gate thresholds.

---

## Task 7: Final Verification and Commit

**Files:**
- All files touched above.

- [ ] **Step 1: Format Go files**

Run:

```bash
gofmt -w internal/generator/generator.go internal/generator/funcs.go internal/generator/generator_test.go
```

Expected: command exits 0.

- [ ] **Step 2: Run script tests**

Run:

```bash
python3 templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py
```

Expected: all report `OK`.

- [ ] **Step 3: Run Go tests**

Run:

```bash
go test ./... -race
./scripts/check-go-coverage.sh
```

Expected: both pass.

- [ ] **Step 4: Run web tests and build**

Run:

```bash
cd web && npm test -- --run
cd web && npm run build
```

Expected: tests and build pass.

- [ ] **Step 5: Run audit and diff checks**

Run:

```bash
make audit
git diff --check
git status --short
```

Expected: audit passes, no whitespace errors, status only lists intended files.

- [ ] **Step 6: Commit**

Stage only intended files:

```bash
git add \
  templates/workspace/skills/config-executor/scripts/kuboard_config.py \
  templates/workspace/skills/config-executor/scripts/test_kuboard_config.py \
  internal/generator/generator.go \
  internal/generator/funcs.go \
  internal/generator/generator_test.go \
  templates/workspace/skills/routing/references/frontend-entry-map.yaml.tmpl \
  templates/workspace/skills/frontend-repro-investigator/scripts/har_analyzer.py \
  templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py \
  templates/workspace/skills/frontend-repro-investigator/scripts/console_analyzer.py \
  templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py \
  templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl \
  CONTRIBUTING.md \
  Makefile \
  .github/workflows/ci.yml \
  .gitlab-ci.yml \
  docs/superpowers/plans/2026-07-03-remaining-risk-fixes.md
git commit -m "fix: close remaining troubleshooting risk gaps"
```

Expected: commit succeeds.

- [ ] **Step 7: Push**

Run:

```bash
git push
```

Expected: current branch pushes successfully and remote CI starts for the branch.

---

## Self-Review

- Spec coverage: Kuboard runtime coverage, frontend endpoint routing, HAR hardening, console/Sentry evidence, CI branch/lint/audit guardrails, and final verification are covered.
- Placeholder scan: no implementation step depends on an unspecified file or unnamed command.
- Type consistency: `FrontendEndpointsByRepo` is added to `Context`, populated by `LoadAnalysisReport`, exposed by `frontendEndpointsForRepo`, and consumed only by the frontend entry template.
