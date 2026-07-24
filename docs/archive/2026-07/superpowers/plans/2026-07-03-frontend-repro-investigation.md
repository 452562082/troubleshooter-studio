# Frontend Repro Investigation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Add a frontend-reproduction investigation path so generated troubleshooters can start from HAR / console / RUM evidence, identify the failing browser request, and continue into existing backend trace/log/runtime workflows.

**Architecture:** Build an artifact-first flow before browser automation. Add one generated skill (`frontend-repro-investigator`) with a stdlib Python HAR analyzer, emit a frontend entry routing reference from repo scan facts, and wire `incident-investigator` to call the frontend skill when client symptoms are reported.

**Tech Stack:** Go templates and generator tests, Python 3 stdlib, existing analyzer notes, generated workspace skills, Vitest/Go tests where existing surfaces are touched.

---

## File Structure

- Create: `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`
  - Owns the frontend evidence workflow and defines the handoff contract to trace/log/runtime skills.
- Create: `templates/workspace/skills/frontend-repro-investigator/scripts/har_analyzer.py`
  - Parses HAR JSON from file or stdin and emits normalized JSON evidence.
- Create: `templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py`
  - Repo-side tests only; generator already filters `test_*.py`.
- Create: `templates/workspace/skills/routing/references/frontend-entry-map.yaml.tmpl`
  - Generated reference for frontend repo facts: framework, API URLs, role, candidate downstream services.
- Modify: `templates/workspace/skills/routing/SKILL.md.tmpl`
  - Add `frontend-entry-map.yaml` to the minimal routing file index.
- Modify: `templates/workspace/skills/incident-investigator/SKILL.md.tmpl`
  - Make client symptoms enter `frontend-repro-investigator` before backend trace/log queries.
- Modify: `internal/generator/generator_test.go`
  - Assert the new skill and reference file are generated, and `test_har_analyzer.py` is filtered from generated workspaces.
- Modify: `internal/analyzer/node_analyzer.go` and `internal/analyzer/node_analyzer_test.go`
  - Keep first phase minimal: use existing `api_url[...]` notes and framework facts; add endpoint path extraction only if the test can stay small and deterministic.
- Modify: `examples/three-tier-troubleshooter.yaml` or `examples/shop-troubleshooter.yaml`
  - Ensure at least one example has a frontend repo that exercises the generated reference.

## Scope

This plan does not implement Playwright/browser automation. The first deliverable accepts user/test artifacts and turns them into backend investigation entry points. Automated browser reproduction becomes a later, separate plan after this artifact-first path is reliable.

### Task 1: HAR Analyzer Script

**Files:**
- Create: `templates/workspace/skills/frontend-repro-investigator/scripts/har_analyzer.py`
- Create: `templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py`

- [x] **Step 1: Write the failing HAR analyzer tests**

Create `templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py`:

```python
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
```

- [x] **Step 2: Run the test and verify it fails**

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py
```

Expected: FAIL because `har_analyzer.py` does not exist.

- [x] **Step 3: Add the minimal HAR analyzer implementation**

Create `templates/workspace/skills/frontend-repro-investigator/scripts/har_analyzer.py`:

```python
#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import re
import sys
from urllib.parse import urlparse


TRACE_HEADERS = {
    "x-trace-id", "trace-id", "x-request-id", "request-id", "x-correlation-id",
    "traceparent",
}
STATIC_EXTENSIONS = (".js", ".css", ".map", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".woff", ".woff2")


def header_values(headers: list[dict], names: set[str]) -> list[str]:
    values = []
    for h in headers or []:
        name = str(h.get("name", "")).lower()
        value = str(h.get("value", "")).strip()
        if name in names and value:
            values.append(value)
    return values


def path_for(url: str) -> str:
    parsed = urlparse(url)
    return parsed.path or "/"


def is_static_asset(url: str) -> bool:
    return path_for(url).lower().endswith(STATIC_EXTENSIONS)


def body_snippet(entry: dict) -> str:
    text = (((entry.get("response") or {}).get("content") or {}).get("text") or "")
    text = re.sub(r"(?i)(token|password|secret)[=:][^,&\\s]+", r"\\1=<redacted>", str(text))
    return text[:500]


def summarize_entry(entry: dict) -> dict:
    req = entry.get("request") or {}
    resp = entry.get("response") or {}
    headers = []
    headers.extend(header_values(req.get("headers") or [], TRACE_HEADERS))
    headers.extend(header_values(resp.get("headers") or [], TRACE_HEADERS))
    return {
        "started_at": entry.get("startedDateTime", ""),
        "method": req.get("method", "GET"),
        "url": req.get("url", ""),
        "path": path_for(req.get("url", "")),
        "status": int(resp.get("status") or 0),
        "duration_ms": int(entry.get("time") or 0),
        "trace_ids": sorted(set(headers)),
        "response_snippet": body_snippet(entry),
    }


def analyze(har: dict) -> dict:
    entries = (((har.get("log") or {}).get("entries")) or [])
    failed = []
    slow = []
    frontend_findings = []
    candidate_endpoints = []
    trace_ids = set()

    for entry in entries:
        item = summarize_entry(entry)
        status = item["status"]
        url = item["url"]
        for tid in item["trace_ids"]:
            trace_ids.add(tid)
        if status >= 400:
            failed.append(item)
            if is_static_asset(url):
                frontend_findings.append({
                    "type": "static_asset_failed",
                    "url": url,
                    "status": status,
                    "hint": "Check frontend deploy version, CDN cache, and stale index.html referencing removed chunks.",
                })
            else:
                candidate_endpoints.append(item["path"])
        if item["duration_ms"] >= 1000 and not is_static_asset(url):
            slow.append(item)
            candidate_endpoints.append(item["path"])

    return {
        "summary": {
            "entry_count": len(entries),
            "failed_request_count": len(failed),
            "slow_request_count": len(slow),
            "frontend_finding_count": len(frontend_findings),
        },
        "failed_requests": failed[:20],
        "slow_requests": sorted(slow, key=lambda x: x["duration_ms"], reverse=True)[:20],
        "frontend_findings": frontend_findings[:20],
        "backend_handoff": {
            "trace_ids": sorted(trace_ids),
            "candidate_endpoints": sorted(set(candidate_endpoints)),
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Analyze HAR evidence for frontend-to-backend troubleshooting.")
    parser.add_argument("--file", help="HAR file path. If omitted, read stdin.")
    args = parser.parse_args()
    raw = open(args.file, "r", encoding="utf-8").read() if args.file else sys.stdin.read()
    payload = analyze(json.loads(raw))
    print(json.dumps(payload, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

- [x] **Step 4: Run the HAR analyzer tests and verify they pass**

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py
```

Expected: `Ran 3 tests ... OK`.

### Task 2: Frontend Repro Skill

**Files:**
- Create: `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`
- Modify: `internal/generator/generator_test.go`

- [x] **Step 1: Write a generator test for the skill and test-file filtering**

Add assertions to the existing generation test that already checks config-executor script filtering:

```go
frontendSkill := filepath.Join(wsRoot, "skills", "frontend-repro-investigator")
if _, err := os.Stat(filepath.Join(frontendSkill, "SKILL.md")); err != nil {
    t.Errorf("frontend-repro-investigator skill should be generated: %v", err)
}
if _, err := os.Stat(filepath.Join(frontendSkill, "scripts", "har_analyzer.py")); err != nil {
    t.Errorf("har_analyzer.py should be generated: %v", err)
}
if _, err := os.Stat(filepath.Join(frontendSkill, "scripts", "test_har_analyzer.py")); err == nil {
    t.Errorf("test_har_analyzer.py should not be generated")
}
```

- [x] **Step 2: Run the targeted generator test and verify it fails**

Run:

```bash
go test ./internal/generator -run TestGenerate
```

Expected: FAIL because the new skill template does not exist yet.

- [x] **Step 3: Create the skill template**

Create `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`:

```markdown
---
name: frontend-repro-investigator
description: 从前端复现证据开始排障。读取 HAR、console、RUM/Sentry 链接或用户复现步骤，提取失败请求、trace/request id、静态资源问题和后端候选接口，再交给 tracing/log/runtime skills 深挖。
---

# Frontend Repro Investigator

## When To Use

Use this skill before backend-only investigation when the report starts from:

- page blank screen, button no response, upload failed, rendering mismatch
- browser-specific or device-specific behavior
- console error, Sentry/RUM/ARMS link, HAR file
- tester says "I reproduced it in the frontend"

Do not start by querying backend trace only. First establish what the browser saw.

## Required Inputs

Ask for the smallest missing item, one question at a time:

- env
- page URL or route
- exact interaction
- occurrence time and timezone
- one artifact: HAR file, console text/screenshot, RUM/Sentry link, or trace/request id

## HAR Workflow

Run from workspace root:

```bash
python3 skills/frontend-repro-investigator/scripts/har_analyzer.py --file <path-to.har>
```

The script returns:

- `failed_requests`: 4xx/5xx browser requests
- `slow_requests`: API requests taking at least 1000 ms
- `frontend_findings`: static chunk/CSS/image failures
- `backend_handoff.trace_ids`: values from `x-trace-id`, `x-request-id`, `traceparent`, and similar headers
- `backend_handoff.candidate_endpoints`: API paths to map into backend services

## Console / RUM Workflow

If the user provides console text or a RUM/Sentry link instead of HAR:

1. Extract message, stack top frame, source file/chunk, release/bundle version, page URL, user/session id, and timestamp.
2. If it is `ChunkLoadError`, `Loading CSS chunk failed`, or a static asset 404, classify as frontend deploy/cache/CDN first.
3. If it contains an API URL, HTTP status, request id, or trace id, continue to backend handoff.
4. If only a JS stack exists, use `routing/references/frontend-entry-map.yaml` to identify the frontend repo and inspect the referenced component/request wrapper.

## Backend Handoff

After HAR/console/RUM analysis:

1. Read `routing/references/frontend-entry-map.yaml`.
2. Match `candidate_endpoints` to known API base URLs and candidate downstream services.
3. If a trace/request id exists, call `tracing-query` and log query skills with the same time window.
4. If no trace id exists, search logs by endpoint path, user id, session id, and occurrence time.
5. If backend did not receive the request, keep the root-cause candidates on frontend/CDN/network/CORS/auth.

## Output Contract

Always return this structure:

```yaml
frontend_evidence:
  page: "<page-url-or-route>"
  action: "<user-action>"
  artifact_type: "har|console|rum|manual"
  browser: "<browser-version-if-known>"
findings:
  - type: "api_5xx|api_4xx|slow_api|static_asset_failed|cors|js_exception|backend_not_reached"
    evidence: "<specific request/error/stack>"
backend_handoff:
  env: "<env>"
  endpoints: []
  trace_ids: []
  candidate_services: []
next_skills:
  - "tracing-query"
  - "elk-log-query"
```

## Hard Constraints

- Only read local artifacts and observability data.
- Redact tokens, cookies, passwords, authorization headers, and personal identifiers before quoting.
- Never claim backend root cause until browser evidence is linked to backend trace/log/runtime evidence.
```

- [x] **Step 4: Run the targeted generator test and verify it passes**

Run:

```bash
go test ./internal/generator -run TestGenerate
```

Expected: PASS.

### Task 3: Frontend Routing Reference

**Files:**
- Create: `templates/workspace/skills/routing/references/frontend-entry-map.yaml.tmpl`
- Modify: `templates/workspace/skills/routing/SKILL.md.tmpl`
- Modify: `internal/generator/generator_test.go`

- [x] **Step 1: Write a generator assertion for the routing reference**

Add this assertion in a generator test using `examples/three-tier-troubleshooter.yaml`:

```go
cfg := loadCfg(t, "examples/three-tier-troubleshooter.yaml")
out := t.TempDir()
tr := filepath.Join(projectRoot(t), "templates")
if err := New(cfg, tr, out).Generate(); err != nil {
    t.Fatalf("generate: %v", err)
}
fm := readFile(t, filepath.Join(out, "templates/workspace-template/skills/routing/references/frontend-entry-map.yaml"))
if !strings.Contains(fm, "frontend_entries:") {
    t.Fatalf("frontend-entry-map should contain frontend_entries root, got:\n%s", fm)
}
if !strings.Contains(fm, "candidate_downstream:") {
    t.Errorf("frontend-entry-map should include candidate_downstream, got:\n%s", fm)
}
```

- [x] **Step 2: Run the targeted test and verify it fails**

Run:

```bash
go test ./internal/generator -run TestGenerate
```

Expected: FAIL because `frontend-entry-map.yaml` is not generated.

- [x] **Step 3: Add the routing reference template**

Create `templates/workspace/skills/routing/references/frontend-entry-map.yaml.tmpl`:

```yaml
# frontend-entry-map.yaml
# 前端复现入口图:把页面/浏览器证据转成 API endpoint 和候选后端服务。
# 来源:
#   - repos[].role in troubleshooter.yaml
#   - node analyzer notes: frontend_framework=..., api_url[...]=...
#   - service-dependency-map downstream edges

frontend_entries:
{{- $ctx := .}}
{{- range .Repos}}
  {{- $role := .EffectiveRole}}
  {{- if or (eq $role "frontend") (eq $role "admin") (eq $role "mobile")}}
  {{.Name}}:
    role: {{$role | printf "%q"}}
    stack: {{.Stack | printf "%q"}}
    framework: {{if .Framework}}{{.Framework | printf "%q"}}{{else}}"unknown"{{end}}
    web_domains:
    {{- range $ctx.Environments}}
      {{.ID}}: {{if .WebDomain}}{{.WebDomain | printf "%q"}}{{else}}""{{end}}
    {{- end}}
    api_domains:
    {{- range $ctx.Environments}}
      {{.ID}}: {{.APIDomain | printf "%q"}}
    {{- end}}
    candidate_downstream:
    {{- $svcs := .ServiceNames}}
    {{- if not $svcs}}{{$svcs = list .Name}}{{end}}
    {{- range $svcs}}
      - {{. | printf "%q"}}
    {{- end}}
    notes:
      - "Use HAR candidate_endpoints to match api_domains, then trace/log by trace_id or endpoint path."
  {{- end}}
{{- end}}
```

- [x] **Step 4: Add the reference to routing skill index**

In `templates/workspace/skills/routing/SKILL.md.tmpl`, add a row to the mapping file table:

```markdown
| 前端复现入口 / 页面到 API 映射 | `references/frontend-entry-map.yaml` |
```

- [x] **Step 5: Run generator tests**

Run:

```bash
go test ./internal/generator -run TestGenerate
```

Expected: PASS.

### Task 4: Incident Investigator Integration

**Files:**
- Modify: `templates/workspace/skills/incident-investigator/SKILL.md.tmpl`
- Modify: `internal/generator/generator_test.go`

- [x] **Step 1: Write a generated content assertion**

Add an assertion that generated `incident-investigator/SKILL.md` contains the frontend skill handoff:

```go
ii := readFile(t, filepath.Join(wsRoot, "skills", "incident-investigator", "SKILL.md"))
if !strings.Contains(ii, "frontend-repro-investigator") {
    t.Errorf("incident-investigator should hand client symptoms to frontend-repro-investigator")
}
```

- [x] **Step 2: Run the targeted test and verify it fails**

Run:

```bash
go test ./internal/generator -run TestGenerate
```

Expected: FAIL until the incident template is updated.

- [x] **Step 3: Update client symptom flow**

In `templates/workspace/skills/incident-investigator/SKILL.md.tmpl`, replace the existing client clarification paragraph with this stricter flow:

```markdown
**1.1c 客户端类问题强制前端取证**:症状含 "白屏 / 卡顿 / 按钮没反应 / 渲染错乱 / 某浏览器/机型独有 /
页面慢 / console 报错 / 上传失败" 等客户端关键词时,先进入 `frontend-repro-investigator`。

最小输入缺失时只问一项:env、页面 URL/route、具体交互、发生时间、HAR/console/RUM/trace_id 之一。
拿到 HAR 时先执行:

```bash
python3 skills/frontend-repro-investigator/scripts/har_analyzer.py --file <path-to.har>
```

根据输出分流:
- `frontend_findings` 非空且无后端 endpoint → 先按前端 bundle/CDN/cache/CORS 定位,同时查发布版本。
- `backend_handoff.trace_ids` 非空 → 调 `tracing-query` 查 trace,再查同窗口日志。
- `backend_handoff.candidate_endpoints` 非空但无 trace → 用 endpoint + 时间窗查日志,并用 `routing/references/frontend-entry-map.yaml` 找候选服务。
- 后端无收到请求证据 → 不得声称后端根因,候选保持在浏览器/CDN/网关/auth/CORS。
```

- [x] **Step 4: Run generator tests**

Run:

```bash
go test ./internal/generator -run TestGenerate
```

Expected: PASS.

### Task 5: Analyzer Enhancement For API Endpoints

**Files:**
- Modify: `internal/analyzer/node_analyzer.go`
- Modify: `internal/analyzer/node_analyzer_test.go`

- [x] **Step 1: Write a failing endpoint extraction test**

Add to `internal/analyzer/node_analyzer_test.go`:

```go
func TestNodeAnalyzer_ExtractsAPIEndpointPaths(t *testing.T) {
    dir := t.TempDir()
    if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"shop-web","dependencies":{"vite":"latest","vue":"latest"}}`), 0o644); err != nil {
        t.Fatal(err)
    }
    if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
        t.Fatal(err)
    }
    src := `fetch("/api/orders"); axios.post('/api/payments/submit');`
    if err := os.WriteFile(filepath.Join(dir, "src", "api.ts"), []byte(src), 0o644); err != nil {
        t.Fatal(err)
    }

    ra, err := NewNodeAnalyzer().Analyze(dir, nil)
    if err != nil {
        t.Fatal(err)
    }
    joined := strings.Join(ra.Notes, "\n")
    if !strings.Contains(joined, "api_endpoint[src/api.ts]=/api/orders") {
        t.Fatalf("expected /api/orders endpoint note, got %v", ra.Notes)
    }
    if !strings.Contains(joined, "api_endpoint[src/api.ts]=/api/payments/submit") {
        t.Fatalf("expected /api/payments/submit endpoint note, got %v", ra.Notes)
    }
}
```

- [x] **Step 2: Run the test and verify it fails**

Run:

```bash
go test ./internal/analyzer -run TestNodeAnalyzer_ExtractsAPIEndpointPaths
```

Expected: FAIL because endpoint path extraction is not implemented.

- [x] **Step 3: Implement bounded endpoint scanning**

Add to `internal/analyzer/node_analyzer.go`:

```go
func extractAPIEndpointPaths(src string) []string {
    re := regexp.MustCompile(`(?i)(fetch|axios\.(get|post|put|patch|delete)|request)\s*\(\s*['"]([^'"]+)['"]`)
    seen := map[string]bool{}
    var out []string
    for _, m := range re.FindAllStringSubmatch(src, -1) {
        path := strings.TrimSpace(m[len(m)-1])
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

Then, inside `Analyze`, scan only small source files:

```go
srcFiles, _ := walkFiles(repoPath, includePaths, func(rel string) bool {
    ext := strings.ToLower(filepath.Ext(rel))
    if ext != ".js" && ext != ".jsx" && ext != ".ts" && ext != ".tsx" && ext != ".vue" {
        return false
    }
    return !strings.Contains(rel, "node_modules")
})
for _, f := range srcFiles {
    info, err := os.Stat(f)
    if err != nil || info.Size() > 512*1024 {
        continue
    }
    b, err := os.ReadFile(f)
    if err != nil {
        continue
    }
    rel, _ := filepath.Rel(repoPath, f)
    for _, ep := range extractAPIEndpointPaths(string(b)) {
        ra.Notes = append(ra.Notes, "api_endpoint["+rel+"]="+ep)
    }
}
```

Update imports:

```go
import (
    "encoding/json"
    "os"
    "path/filepath"
    "regexp"
    "sort"
    "strings"
)
```

- [x] **Step 4: Run analyzer tests**

Run:

```bash
go test ./internal/analyzer
```

Expected: PASS.

### Task 6: End-to-End Generated Workspace Checks

**Files:**
- Modify: `internal/generator/generator_test.go`
- Modify: `examples/three-tier-troubleshooter.yaml` if it lacks the needed frontend role data.

- [x] **Step 1: Add a focused generated workspace contract test**

Add:

```go
func TestGenerate_FrontendReproArtifacts(t *testing.T) {
    cfg := loadCfg(t, "examples/three-tier-troubleshooter.yaml")
    out := t.TempDir()
    tr := filepath.Join(projectRoot(t), "templates")
    if err := New(cfg, tr, out).Generate(); err != nil {
        t.Fatalf("generate: %v", err)
    }
    root := filepath.Join(out, "templates/workspace-template")
    assertExists(t, root, []string{
        "skills/frontend-repro-investigator/SKILL.md",
        "skills/frontend-repro-investigator/scripts/har_analyzer.py",
        "skills/routing/references/frontend-entry-map.yaml",
    })
    assertNotExists(t, root, []string{
        "skills/frontend-repro-investigator/scripts/test_har_analyzer.py",
    })
}
```

Add helper if absent:

```go
func assertNotExists(t *testing.T, root string, rels []string) {
    t.Helper()
    for _, rel := range rels {
        if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
            t.Fatalf("%s should not exist", rel)
        }
    }
}
```

- [x] **Step 2: Run the generated workspace contract test**

Run:

```bash
go test ./internal/generator -run TestGenerate_FrontendReproArtifacts
```

Expected: PASS.

### Task 7: Documentation And Verification

**Files:**
- Modify: `CONTRIBUTING.md`
- Modify: `docs/troubleshooting-flow.md`

- [x] **Step 1: Document the frontend-first evidence rule**

Add this sentence to `docs/troubleshooting-flow.md` near the existing client-problem guidance:

```markdown
客户端复现问题先走 `frontend-repro-investigator`:HAR/console/RUM 任一证据 → 提取失败请求/trace_id/静态资源问题 → 再接 backend trace/log/runtime;没有浏览器证据时不得只凭后端 trace 下结论。
```

- [x] **Step 2: Document the new repo-side test command**

Add to `CONTRIBUTING.md` testing commands:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py
```

- [x] **Step 3: Run focused verification**

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py
go test ./internal/analyzer
go test ./internal/generator -run 'TestGenerate|TestGenerate_FrontendReproArtifacts'
```

Expected: all commands exit 0.

- [x] **Step 4: Run full verification**

Run:

```bash
go test ./... -race
./scripts/check-go-coverage.sh
cd web && npm test -- --run
cd web && npm run build
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 5: Commit**

Use explicit paths:

```bash
git add templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl \
  templates/workspace/skills/frontend-repro-investigator/scripts/har_analyzer.py \
  templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py \
  templates/workspace/skills/routing/references/frontend-entry-map.yaml.tmpl \
  templates/workspace/skills/routing/SKILL.md.tmpl \
  templates/workspace/skills/incident-investigator/SKILL.md.tmpl \
  internal/analyzer/node_analyzer.go \
  internal/analyzer/node_analyzer_test.go \
  internal/generator/generator_test.go \
  examples/three-tier-troubleshooter.yaml \
  docs/troubleshooting-flow.md \
  CONTRIBUTING.md
git commit -m "feat: add frontend repro investigation path"
```

## Self-Review

- Spec coverage: HAR/console/RUM artifact intake is covered by Tasks 1 and 2; routing reference by Task 3; incident integration by Task 4; analyzer endpoint discovery by Task 5; generation and docs by Tasks 6 and 7.
- Placeholder scan: no unresolved placeholder markers or unspecified error handling remains in this plan.
- Type consistency: generated skill name is consistently `frontend-repro-investigator`; script path is consistently `skills/frontend-repro-investigator/scripts/har_analyzer.py`; routing reference is consistently `frontend-entry-map.yaml`.
