# Next Optimization Wave Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade generated troubleshooting bots from "frontend evidence produces candidate backend services" to "frontend evidence produces precise route/service handoff, richer browser artifacts, provider-backed event lookup, broader runtime parsing, and stronger generated-workspace verification."

**Architecture:** Keep the existing generated-workspace model. Repository analyzers extract route and endpoint facts at build time; generator context renders those facts into routing references; generated scripts parse browser artifacts and provider events locally with safe redaction. Browser automation remains optional and dependency-gated so generated bots still work without Playwright installed.

**Tech Stack:** Go analyzer/generator tests, Go templates, Python 3 stdlib scripts, optional Node.js Playwright runner, Vitest/web build, shell coverage gate.

---

## Files To Modify

- Modify `internal/analyzer/types.go`: add `APIRoute` and `RepoAnalysis.APIRoutes`.
- Create `internal/analyzer/api_route_scan.go`: language-neutral route normalization and matching helpers.
- Create `internal/analyzer/api_route_scan_test.go`: route extraction and frontend endpoint matching tests.
- Modify `internal/analyzerpipe/pipeline.go`: call `ScanAPIRoutes` during repository analysis.
- Modify `internal/generator/generator.go`: add `APIRoutesByRepo` to `Context` and load analysis routes.
- Modify `internal/generator/funcs.go`: add `frontendRouteCandidatesForRepoEndpoint`.
- Modify `templates/workspace/skills/routing/references/frontend-entry-map.yaml.tmpl`: render route-matched services before broad candidates.
- Modify `internal/generator/generator_test.go`: assert generated routing map contains exact/pattern route candidates.
- Create `templates/workspace/skills/frontend-repro-investigator/scripts/browser_collect.mjs`: optional Playwright artifact collector.
- Create `templates/workspace/skills/frontend-repro-investigator/scripts/test_browser_collect.py`: test CLI planning and dependency-gated output without launching a browser.
- Create `templates/workspace/skills/frontend-repro-investigator/scripts/sentry_fetch.py`: fetch and normalize Sentry events using token/env or pasted event URL.
- Create `templates/workspace/skills/frontend-repro-investigator/scripts/test_sentry_fetch.py`: mock HTTP Sentry API tests and redaction tests.
- Modify `templates/workspace/skills/frontend-repro-investigator/scripts/har_analyzer.py`: add chunk/cache/CSP/mixed-content/source-map related findings.
- Modify `templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py`: add those evidence cases.
- Modify `templates/workspace/skills/frontend-repro-investigator/scripts/console_analyzer.py`: classify ChunkLoadError, CSP, mixed content, and source-map references from console text.
- Modify `templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py`: add console evidence tests.
- Modify `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`: document browser collector and Sentry fetch workflow.
- Modify `templates/workspace/skills/config-executor/scripts/kuboard_config.py`: add URI/DSN parsing for runtime blocks.
- Modify `templates/workspace/skills/config-executor/scripts/test_kuboard_config.py`: add URI/DSN coverage.
- Modify `internal/generator/generator_test.go`: add generated-workspace E2E assertions for shipped scripts and filtered test files.
- Modify `CONTRIBUTING.md`: add new script tests and generated E2E target.
- Modify `scripts/check-go-coverage.sh`: raise package thresholds only after new tests pass.

---

## Task 1: Route-to-Service Precision

**Files:**
- Modify `internal/analyzer/types.go`
- Create `internal/analyzer/api_route_scan.go`
- Create `internal/analyzer/api_route_scan_test.go`
- Modify `internal/analyzerpipe/pipeline.go`

- [ ] **Step 1: Add route model types**

Add this type to `internal/analyzer/types.go` near `DownstreamCall` and add `APIRoutes []APIRoute` to `RepoAnalysis`:

```go
type APIRoute struct {
	Path     string `json:"path"`
	Method   string `json:"method,omitempty"`
	Source   string `json:"source,omitempty"`
	Line     int    `json:"line,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	Strength string `json:"strength,omitempty"`
}
```

- [ ] **Step 2: Write route extraction tests**

Create `internal/analyzer/api_route_scan_test.go` with:

```go
package analyzer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractAPIRoutesFromGoAndNodeSources(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "handler.go"), []byte(`
package main
func main() {
  r.GET("/api/orders/:id", handler)
  r.POST("/api/payments/submit", handler)
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "routes.ts"), []byte(`
router.get('/api/users/:id', handler)
app.post("/graphql", handler)
`), 0o644); err != nil {
		t.Fatal(err)
	}

	routes := ScanAPIRoutes("go", root, nil)
	routes = append(routes, ScanAPIRoutes("node", root, nil)...)
	got := map[string]bool{}
	for _, route := range routes {
		got[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"GET /api/orders/:id",
		"POST /api/payments/submit",
		"GET /api/users/:id",
		"POST /graphql",
	} {
		if !got[want] {
			t.Fatalf("missing route %s in %#v", want, routes)
		}
	}
}

func TestRouteMatchStrength(t *testing.T) {
	cases := []struct {
		route string
		path  string
		want  string
	}{
		{"/api/orders/:id", "/api/orders/42", "pattern"},
		{"/api/payments/submit", "/api/payments/submit", "exact"},
		{"/api/orders", "/api/orders/42", "prefix"},
		{"/api/users/:id", "/api/orders/42", ""},
	}
	for _, tc := range cases {
		if got := routeMatchStrength(tc.route, tc.path); got != tc.want {
			t.Fatalf("routeMatchStrength(%q,%q)=%q, want %q", tc.route, tc.path, got, tc.want)
		}
	}
}
```

- [ ] **Step 3: Run failing analyzer tests**

Run:

```bash
go test ./internal/analyzer -run 'TestExtractAPIRoutes|TestRouteMatchStrength'
```

Expected: fail because `ScanAPIRoutes` and `routeMatchStrength` do not exist.

- [ ] **Step 4: Implement route scanner**

Create `internal/analyzer/api_route_scan.go`:

```go
package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var routePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:GET|POST|PUT|PATCH|DELETE)\s*\(\s*["']([^"']+)["']`),
	regexp.MustCompile(`(?i)\b(?:router|app|r)\.(get|post|put|patch|delete)\s*\(\s*["']([^"']+)["']`),
	regexp.MustCompile(`@(GetMapping|PostMapping|PutMapping|PatchMapping|DeleteMapping|RequestMapping)\s*\(\s*(?:value\s*=\s*)?["']([^"']+)["']`),
	regexp.MustCompile(`(?i)\b(?:route|path)\s*[:=]\s*["']([^"']+)["']`),
}

func ScanAPIRoutes(stack, repoPath string, includePaths []string) []APIRoute {
	files, _ := walkFiles(repoPath, includePaths, func(rel string) bool {
		if strings.Contains(rel, "node_modules") || strings.Contains(rel, "vendor") {
			return false
		}
		ext := strings.ToLower(filepath.Ext(rel))
		switch stack {
		case "go":
			return ext == ".go"
		case "node":
			return ext == ".js" || ext == ".jsx" || ext == ".ts" || ext == ".tsx"
		case "java":
			return ext == ".java" || ext == ".kt"
		case "python":
			return ext == ".py"
		default:
			return ext == ".go" || ext == ".java" || ext == ".kt" || ext == ".py" || ext == ".js" || ext == ".ts" || ext == ".tsx"
		}
	})
	seen := map[string]bool{}
	var out []APIRoute
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil || info.Size() > 512*1024 {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(repoPath, file)
		for _, route := range extractRoutesFromSource(string(data), rel) {
			key := route.Method + " " + route.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, route)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path == out[j].Path {
			return out[i].Method < out[j].Method
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func extractRoutesFromSource(src, source string) []APIRoute {
	var routes []APIRoute
	for _, re := range routePatterns {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			method, path := "ANY", ""
			if len(m) == 2 {
				path = m[1]
			}
			if len(m) >= 3 {
				method = strings.ToUpper(strings.TrimSuffix(strings.TrimPrefix(m[1], "Get"), "Mapping"))
				method = strings.TrimSuffix(method, "MAPPING")
				if method == "" || method == "REQUEST" {
					method = "ANY"
				}
				path = m[2]
			}
			path = normalizeRoutePath(path)
			if path == "" {
				continue
			}
			routes = append(routes, APIRoute{
				Path:     path,
				Method:   method,
				Source:   source,
				Pattern:  routePatternFor(path),
				Strength: "scanned",
			})
		}
	}
	return routes
}

func normalizeRoutePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if path != "/graphql" && !strings.HasPrefix(path, "/api/") && path != "/api" {
		return ""
	}
	return strings.TrimRight(path, "/")
}

func routePatternFor(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") || strings.HasPrefix(part, "{") || strings.HasPrefix(part, "<") {
			parts[i] = "*"
		}
	}
	return strings.Join(parts, "/")
}

func routeMatchStrength(routePath, endpointPath string) string {
	routePath = normalizeRoutePath(routePath)
	endpointPath = normalizeRoutePath(endpointPath)
	if routePath == "" || endpointPath == "" {
		return ""
	}
	if routePath == endpointPath {
		return "exact"
	}
	routeParts := strings.Split(strings.Trim(routePath, "/"), "/")
	endpointParts := strings.Split(strings.Trim(endpointPath, "/"), "/")
	if len(routeParts) == len(endpointParts) {
		ok := true
		for i := range routeParts {
			rp := routeParts[i]
			if strings.HasPrefix(rp, ":") || strings.HasPrefix(rp, "{") || strings.HasPrefix(rp, "<") {
				continue
			}
			if rp != endpointParts[i] {
				ok = false
				break
			}
		}
		if ok {
			return "pattern"
		}
	}
	if strings.HasPrefix(endpointPath, strings.TrimRight(routePath, "/")+"/") {
		return "prefix"
	}
	return ""
}
```

- [ ] **Step 5: Wire route scan into analyzer pipeline**

In `internal/analyzerpipe/pipeline.go`, after dependency scanning, add:

```go
ra.APIRoutes = analyzer.ScanAPIRoutes(effectiveStack, repoPath, repo.Analysis.IncludePaths)
```

- [ ] **Step 6: Run analyzer tests**

Run:

```bash
go test ./internal/analyzer ./internal/analyzerpipe
```

Expected: pass.

---

## Task 2: Render Precise Endpoint Candidates

**Files:**
- Modify `internal/generator/generator.go`
- Modify `internal/generator/funcs.go`
- Modify `templates/workspace/skills/routing/references/frontend-entry-map.yaml.tmpl`
- Modify `internal/generator/generator_test.go`

- [ ] **Step 1: Write generator test for exact/pattern route candidates**

Add to `internal/generator/generator_test.go`:

```go
func TestGenerate_FrontendEntryMapMatchesBackendRoutes(t *testing.T) {
	cfg := loadCfg(t, "examples/three-tier-troubleshooter.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	g := New(cfg, tr, out)
	g.LoadAnalysisReport(analyzer.Report{Repos: []analyzer.RepoAnalysis{
		{
			Name: "mall-web",
			Notes: []string{
				"api_endpoint[src/api.ts]=/api/orders/42",
				"api_endpoint[src/pay.ts]=/api/payments/submit",
			},
		},
		{
			Name:         "order-service",
			ServiceNames: []string{"order-service"},
			APIRoutes: []analyzer.APIRoute{
				{Path: "/api/orders/:id", Method: "GET", Source: "handler.go", Strength: "scanned"},
			},
		},
		{
			Name:         "payment-service",
			ServiceNames: []string{"payment-service"},
			APIRoutes: []analyzer.APIRoute{
				{Path: "/api/payments/submit", Method: "POST", Source: "routes.ts", Strength: "scanned"},
			},
		},
	}})
	if err := g.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	entries := loadFrontendEntryMap(t, filepath.Join(out, "templates/workspace-template/skills/routing/references/frontend-entry-map.yaml"))
	candidates := entries.FrontendEntries["mall-web"].PathCandidates
	orderCandidate := candidates["/api/orders/42"].RouteCandidates[0]
	if orderCandidate.Service != "order-service" || orderCandidate.Match != "pattern" {
		t.Fatalf("order route candidate = %#v", orderCandidate)
	}
	paymentCandidate := candidates["/api/payments/submit"].RouteCandidates[0]
	if paymentCandidate.Service != "payment-service" || paymentCandidate.Match != "exact" {
		t.Fatalf("payment route candidate = %#v", paymentCandidate)
	}
}
```

Extend the local test fixtures:

```go
type pathCandidateFixture struct {
	CandidateServices interface{}             `yaml:"candidate_services"`
	RouteCandidates   []routeCandidateFixture `yaml:"route_candidates"`
}

type routeCandidateFixture struct {
	Service string `yaml:"service"`
	Match   string `yaml:"match"`
	Route   string `yaml:"route"`
	Source  string `yaml:"source"`
}
```

- [ ] **Step 2: Run failing generator test**

Run:

```bash
go test ./internal/generator -run TestGenerate_FrontendEntryMapMatchesBackendRoutes
```

Expected: fail because route candidates are not loaded or rendered.

- [ ] **Step 3: Add generator context state**

In `internal/generator/generator.go`, add to `Context`:

```go
APIRoutesByRepo map[string][]analyzer.APIRoute
```

Initialize in `New`:

```go
APIRoutesByRepo: map[string][]analyzer.APIRoute{},
```

Load in `LoadAnalysisReport`:

```go
if len(ra.APIRoutes) > 0 {
	g.Ctx.APIRoutesByRepo[ra.Name] = ra.APIRoutes
}
```

- [ ] **Step 4: Add route candidate helper**

In `internal/generator/funcs.go`, add:

```go
type frontendRouteCandidate struct {
	Service string
	Match   string
	Route   string
	Method  string
	Source  string
}

func frontendRouteCandidatesForRepoEndpoint(ctx *Context, frontendRepo, endpoint string) []frontendRouteCandidate {
	if ctx == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []frontendRouteCandidate
	for _, repo := range ctx.Repos {
		if repo.Name == frontendRepo || !repo.RequiresServiceNames() {
			continue
		}
		names := repo.ServiceNames
		if len(names) == 0 {
			names = []string{repo.Name}
		}
		for _, route := range ctx.APIRoutesByRepo[repo.Name] {
			match := routeMatchStrengthForTemplate(route.Path, endpoint)
			if match == "" {
				continue
			}
			for _, svc := range names {
				key := svc + "|" + route.Path + "|" + match
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, frontendRouteCandidate{
					Service: svc,
					Match:   match,
					Route:   route.Path,
					Method:  route.Method,
					Source:  route.Source,
				})
			}
		}
	}
	return out
}

func routeMatchStrengthForTemplate(routePath, endpointPath string) string {
	routeParts := strings.Split(strings.Trim(routePath, "/"), "/")
	endpointParts := strings.Split(strings.Trim(endpointPath, "/"), "/")
	if routePath == endpointPath {
		return "exact"
	}
	if len(routeParts) == len(endpointParts) {
		ok := true
		for i := range routeParts {
			part := routeParts[i]
			if strings.HasPrefix(part, ":") || strings.HasPrefix(part, "{") || strings.HasPrefix(part, "<") || part == "*" {
				continue
			}
			if part != endpointParts[i] {
				ok = false
				break
			}
		}
		if ok {
			return "pattern"
		}
	}
	if strings.HasPrefix(endpointPath, strings.TrimRight(routePath, "/")+"/") {
		return "prefix"
	}
	return ""
}
```

Add to `funcMap`:

```go
"frontendRouteCandidatesForRepoEndpoint": frontendRouteCandidatesForRepoEndpoint,
```

- [ ] **Step 5: Render route candidates**

In `templates/workspace/skills/routing/references/frontend-entry-map.yaml.tmpl`, inside each endpoint under `path_candidates`, before `candidate_services`, render:

```gotemplate
        route_candidates:
        {{- range frontendRouteCandidatesForRepoEndpoint $ctx $repo.Name .}}
          - service: {{.Service | printf "%q"}}
            match: {{.Match | printf "%q"}}
            route: {{.Route | printf "%q"}}
            method: {{.Method | printf "%q"}}
            source: {{.Source | printf "%q"}}
        {{- else}}
          []
        {{- end}}
```

- [ ] **Step 6: Run generator tests**

Run:

```bash
gofmt -w internal/generator/generator.go internal/generator/funcs.go internal/generator/generator_test.go
go test ./internal/generator -run 'TestGenerate_FrontendEntryMap'
```

Expected: pass.

---

## Task 3: Optional Browser Artifact Collector

**Files:**
- Create `templates/workspace/skills/frontend-repro-investigator/scripts/browser_collect.mjs`
- Create `templates/workspace/skills/frontend-repro-investigator/scripts/test_browser_collect.py`
- Modify `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`
- Modify `CONTRIBUTING.md`

- [ ] **Step 1: Add collector tests**

Create `templates/workspace/skills/frontend-repro-investigator/scripts/test_browser_collect.py`:

```python
#!/usr/bin/env python3
import json
import subprocess
import sys
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("browser_collect.mjs")


class BrowserCollectTest(unittest.TestCase):
    def run_script(self, *args):
        return subprocess.run(
            ["node", str(SCRIPT), *args],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )

    def test_plan_outputs_artifact_paths_without_browser_launch(self):
        res = self.run_script("--url", "https://shop.example.com/orders/42", "--out", "/tmp/tshoot-artifacts", "--plan")
        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        self.assertEqual(payload["mode"], "plan")
        self.assertEqual(payload["url"], "https://shop.example.com/orders/42")
        self.assertTrue(payload["artifacts"]["har"].endswith("network.har"))
        self.assertTrue(payload["artifacts"]["console"].endswith("console.jsonl"))

    def test_missing_url_returns_json_error(self):
        res = self.run_script("--plan")
        self.assertNotEqual(res.returncode, 0)
        payload = json.loads(res.stdout)
        self.assertIn("error", payload)


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run failing collector tests**

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_browser_collect.py
```

Expected: fail because `browser_collect.mjs` does not exist.

- [ ] **Step 3: Create browser collector script**

Create `templates/workspace/skills/frontend-repro-investigator/scripts/browser_collect.mjs`:

```js
#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";

function argValue(args, name, fallback = "") {
  const idx = args.indexOf(name);
  return idx >= 0 && idx + 1 < args.length ? args[idx + 1] : fallback;
}

function hasArg(args, name) {
  return args.includes(name);
}

function jsonOut(payload, code = 0) {
  process.stdout.write(JSON.stringify(payload, null, 2) + "\n");
  process.exit(code);
}

const args = process.argv.slice(2);
const url = argValue(args, "--url");
const outDir = argValue(args, "--out", "tshoot-browser-artifacts");
if (!url) {
  jsonOut({ error: "missing --url", hint: "Usage: node browser_collect.mjs --url <page-url> --out <dir>" }, 2);
}

const artifacts = {
  har: path.join(outDir, "network.har"),
  console: path.join(outDir, "console.jsonl"),
  screenshot: path.join(outDir, "screenshot.png"),
  trace: path.join(outDir, "trace.zip"),
};

if (hasArg(args, "--plan")) {
  jsonOut({ mode: "plan", url, artifacts });
}

let chromium;
try {
  ({ chromium } = await import("playwright"));
} catch (err) {
  jsonOut({
    error: "playwright not installed",
    hint: "Run `npm install playwright` in a scratch directory, then rerun this collector.",
    url,
    artifacts,
  }, 3);
}

fs.mkdirSync(outDir, { recursive: true });
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ recordHar: { path: artifacts.har, content: "embed" } });
const page = await context.newPage();
const consoleStream = fs.createWriteStream(artifacts.console, { flags: "a" });
page.on("console", msg => {
  consoleStream.write(JSON.stringify({
    type: msg.type(),
    text: msg.text(),
    location: msg.location(),
    ts: new Date().toISOString(),
  }) + "\n");
});
page.on("pageerror", err => {
  consoleStream.write(JSON.stringify({
    type: "pageerror",
    text: String(err && err.stack ? err.stack : err),
    ts: new Date().toISOString(),
  }) + "\n");
});
await context.tracing.start({ screenshots: true, snapshots: true });
await page.goto(url, { waitUntil: "networkidle", timeout: 45000 });
await page.screenshot({ path: artifacts.screenshot, fullPage: true });
await context.tracing.stop({ path: artifacts.trace });
await context.close();
await browser.close();
consoleStream.end();
jsonOut({ mode: "collected", url, artifacts });
```

- [ ] **Step 4: Document collector workflow**

In `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`, add:

```markdown
Optional browser collection:

```bash
node skills/frontend-repro-investigator/scripts/browser_collect.mjs --url <page-url> --out /tmp/tshoot-browser
python3 skills/frontend-repro-investigator/scripts/har_analyzer.py --file /tmp/tshoot-browser/network.har
python3 skills/frontend-repro-investigator/scripts/console_analyzer.py --file /tmp/tshoot-browser/console.jsonl
```

If Playwright is missing, the collector returns a JSON error with the install hint and does not block HAR/console manual analysis.
```

- [ ] **Step 5: Add test command**

Add to `CONTRIBUTING.md`:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_browser_collect.py
```

- [ ] **Step 6: Run collector tests**

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_browser_collect.py
```

Expected: pass.

---

## Task 4: Sentry Event Fetch and Normalize

**Files:**
- Create `templates/workspace/skills/frontend-repro-investigator/scripts/sentry_fetch.py`
- Create `templates/workspace/skills/frontend-repro-investigator/scripts/test_sentry_fetch.py`
- Modify `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`
- Modify `CONTRIBUTING.md`

- [ ] **Step 1: Add Sentry fetch tests**

Create `templates/workspace/skills/frontend-repro-investigator/scripts/test_sentry_fetch.py`:

```python
#!/usr/bin/env python3
import json
import os
import subprocess
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


SCRIPT = Path(__file__).with_name("sentry_fetch.py")


class MockSentry(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        return

    def do_GET(self):
        if self.headers.get("Authorization") != "Bearer token-123":
            self.send_response(401)
            self.end_headers()
            return
        body = {
            "eventID": "evt-1",
            "message": "Request failed",
            "request": {"url": "https://api.example.com/api/profile?access_token=hidden-token"},
            "contexts": {"trace": {"trace_id": "trace-1"}},
            "exception": {"values": [{"stacktrace": {"frames": [{"filename": "app.js", "function": "loadProfile"}]}}]},
        }
        raw = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


class SentryFetchTest(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), MockSentry)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.base = f"http://127.0.0.1:{self.server.server_port}"

    def tearDown(self):
        self.server.shutdown()
        self.thread.join(timeout=2)
        self.server.server_close()

    def run_script(self, *args):
        env = os.environ.copy()
        env["SENTRY_AUTH_TOKEN"] = "token-123"
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args],
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )

    def test_fetches_event_and_outputs_backend_handoff(self):
        res = self.run_script("--base-url", self.base, "--organization", "org", "--project", "web", "--event-id", "evt-1")
        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        raw = res.stdout
        payload = json.loads(raw)
        self.assertIn("/api/profile", payload["backend_handoff"]["candidate_endpoints"])
        self.assertIn("trace-1", payload["backend_handoff"]["trace_ids"])
        self.assertNotIn("hidden-token", raw)

    def test_missing_token_returns_json_error(self):
        env = os.environ.copy()
        env.pop("SENTRY_AUTH_TOKEN", None)
        res = subprocess.run(
            [sys.executable, str(SCRIPT), "--base-url", self.base, "--organization", "org", "--project", "web", "--event-id", "evt-1"],
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        self.assertEqual(res.returncode, 2)
        self.assertIn("SENTRY_AUTH_TOKEN", res.stdout)


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run failing Sentry tests**

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_sentry_fetch.py
```

Expected: fail because `sentry_fetch.py` does not exist.

- [ ] **Step 3: Implement Sentry fetch script**

Create `templates/workspace/skills/frontend-repro-investigator/scripts/sentry_fetch.py`:

```python
#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import urllib.parse
import urllib.request


def redact(text: str) -> str:
    text = re.sub(r"(?i)([?&](?:access_token|token|secret|password|key)=)[^&\s\"']+", r"\1<redacted>", str(text))
    text = re.sub(r"(?i)(bearer\s+)[a-z0-9._~+/=-]+", r"\1<redacted>", text)
    return text


def error_out(message: str, code: int = 2) -> int:
    print(json.dumps({"error": message}, ensure_ascii=False, indent=2))
    return code


def endpoint_for_url(url: str) -> str:
    parsed = urllib.parse.urlparse(url)
    path = parsed.path or ""
    if path == "/graphql" or path.startswith("/api/") or path == "/api":
        return path
    return ""


def collect_strings(value):
    if isinstance(value, dict):
        for key, item in value.items():
            yield str(key)
            yield from collect_strings(item)
    elif isinstance(value, list):
        for item in value:
            yield from collect_strings(item)
    elif isinstance(value, (str, int, float)):
        yield str(value)


def collect_trace_ids(value):
    traces = []
    if isinstance(value, dict):
        for key, item in value.items():
            lower = str(key).lower().replace("_", "")
            if lower in {"traceid", "xtraceid", "xrequestid", "requestid"} and item:
                traces.append(str(item))
            traces.extend(collect_trace_ids(item))
    elif isinstance(value, list):
        for item in value:
            traces.extend(collect_trace_ids(item))
    return traces


def normalize_event(event: dict) -> dict:
    text = "\n".join(collect_strings(event))
    endpoints = sorted({endpoint_for_url(url) for url in re.findall(r"https?://[^\s\"'<>]+", text)})
    endpoints = [item for item in endpoints if item]
    traces = sorted(set(collect_trace_ids(event)))
    return {
        "summary": {
            "event_id": event.get("eventID") or event.get("event_id") or "",
            "message": redact(event.get("message") or event.get("title") or ""),
        },
        "frontend_findings": [{
            "type": "sentry_event",
            "message": redact(event.get("message") or event.get("title") or ""),
        }],
        "backend_handoff": {
            "trace_ids": traces,
            "candidate_endpoints": endpoints,
        },
        "redacted_event_preview": redact(json.dumps(event, ensure_ascii=False, sort_keys=True))[:1500],
    }


def fetch_event(base_url: str, organization: str, project: str, event_id: str, token: str) -> dict:
    base = base_url.rstrip("/")
    path = f"/api/0/projects/{urllib.parse.quote(organization)}/{urllib.parse.quote(project)}/events/{urllib.parse.quote(event_id)}/"
    req = urllib.request.Request(base + path, headers={"Authorization": f"Bearer {token}", "Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=20) as resp:
        return json.loads(resp.read().decode("utf-8", errors="replace"))


def main() -> int:
    parser = argparse.ArgumentParser(description="Fetch and normalize a Sentry event for troubleshooting handoff.")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--organization", required=True)
    parser.add_argument("--project", required=True)
    parser.add_argument("--event-id", required=True)
    parser.add_argument("--token", default=os.environ.get("SENTRY_AUTH_TOKEN", ""))
    args = parser.parse_args()
    if not args.token:
        return error_out("SENTRY_AUTH_TOKEN missing; pass --token or export SENTRY_AUTH_TOKEN")
    try:
        event = fetch_event(args.base_url, args.organization, args.project, args.event_id, args.token)
    except Exception as exc:
        return error_out(f"fetch sentry event failed: {exc}", code=3)
    print(json.dumps(normalize_event(event), ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

- [ ] **Step 4: Document Sentry workflow**

Add to `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`:

```markdown
Sentry event lookup:

```bash
SENTRY_AUTH_TOKEN=<token> python3 skills/frontend-repro-investigator/scripts/sentry_fetch.py \
  --base-url https://sentry.example.com \
  --organization <org> \
  --project <project> \
  --event-id <event-id>
```

Use the normalized `backend_handoff` in the same way as HAR and console analyzer output.
```

- [ ] **Step 5: Add test command and run tests**

Add to `CONTRIBUTING.md`:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_sentry_fetch.py
```

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_sentry_fetch.py
```

Expected: pass.

---

## Task 5: Broaden Kuboard Runtime DSN Parsing

**Files:**
- Modify `templates/workspace/skills/config-executor/scripts/kuboard_config.py`
- Modify `templates/workspace/skills/config-executor/scripts/test_kuboard_config.py`

- [ ] **Step 1: Add DSN regression test**

Add to `test_kuboard_config.py`:

```python
    def test_runtime_resolution_parses_common_dsn_values(self):
        runtime = kuboard_config.resolve_runtime({
            "DATABASE_URL": "postgres://pg_user:pg_pass@pg.internal:5434/orders?sslmode=disable",
            "REDIS_URL": "redis://:redis-pass@redis.internal:6380/0",
            "RABBITMQ_URL": "amqp://mq_user:mq_pass@mq.internal:5678/orders",
            "CLICKHOUSE_DSN": "http://ch.internal:8124/default",
            "SPRING_KAFKA_BOOTSTRAP_SERVERS": "kafka-1:9092,kafka-2:9092",
        })
        self.assertEqual(runtime["postgres"]["host"], "pg.internal")
        self.assertEqual(runtime["postgres"]["port"], 5434)
        self.assertEqual(runtime["postgres"]["database"], "orders")
        self.assertEqual(runtime["postgres"]["user"], "pg_user")
        self.assertEqual(runtime["redis"]["host"], "redis.internal")
        self.assertEqual(runtime["redis"]["port"], 6380)
        self.assertEqual(runtime["rabbitmq"]["host"], "mq.internal")
        self.assertEqual(runtime["rabbitmq"]["port"], 5678)
        self.assertEqual(runtime["rabbitmq"]["vhost"], "orders")
        self.assertEqual(runtime["clickhouse"]["hosts"], ["http://ch.internal:8124/default"])
        self.assertEqual(runtime["kafka"]["bootstrap_servers"], ["kafka-1:9092", "kafka-2:9092"])
```

Import the module at the top of the test:

```python
import importlib.util

spec = importlib.util.spec_from_file_location("kuboard_config", SCRIPT)
kuboard_config = importlib.util.module_from_spec(spec)
spec.loader.exec_module(kuboard_config)
```

- [ ] **Step 2: Run failing Kuboard test**

Run:

```bash
python3 templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
```

Expected: fail because DSN fields are not parsed.

- [ ] **Step 3: Implement DSN parsing**

In `kuboard_config.py`, add:

```python
from urllib.parse import urlparse, unquote


def parsed_url(value: str):
    try:
        return urlparse(str(value))
    except Exception:
        return None


def db_name_from_path(path: str) -> str:
    return unquote(str(path).lstrip("/").split("/", 1)[0])
```

Extend `resolve_runtime` by reading:

```python
database_url = first_value(data, "DATABASE_URL", "SPRING_DATASOURCE_URL")
redis_url = first_value(data, "REDIS_URL", "SPRING_REDIS_URL")
rabbitmq_url = first_value(data, "RABBITMQ_URL", "AMQP_URL", "SPRING_RABBITMQ_URL")
clickhouse_dsn = first_value(data, "CLICKHOUSE_DSN", "CLICKHOUSE_URL")
```

Before returning, derive fallback values:

```python
db_dsn = parsed_url(database_url)
if db_dsn and db_dsn.scheme in {"postgres", "postgresql"}:
    postgres_host = postgres_host or db_dsn.hostname or ""
    postgres_port = postgres_port or (str(db_dsn.port) if db_dsn.port else "")
    postgres_db = postgres_db or db_name_from_path(db_dsn.path)
    postgres_user = postgres_user or unquote(db_dsn.username or "")
elif db_dsn and db_dsn.scheme in {"mysql", "mariadb"}:
    mysql_host = mysql_host or db_dsn.hostname or ""
    mysql_port = mysql_port or (str(db_dsn.port) if db_dsn.port else "")
    mysql_db = mysql_db or db_name_from_path(db_dsn.path)
    mysql_user = mysql_user or unquote(db_dsn.username or "")

redis_dsn = parsed_url(redis_url)
if redis_dsn and redis_dsn.hostname:
    redis_host = redis_host or redis_dsn.hostname
    redis_port = redis_port or (str(redis_dsn.port) if redis_dsn.port else "")

rabbit_dsn = parsed_url(rabbitmq_url)
if rabbit_dsn and rabbit_dsn.hostname:
    rabbitmq_host = rabbitmq_host or rabbit_dsn.hostname
    rabbitmq_port = rabbitmq_port or (str(rabbit_dsn.port) if rabbit_dsn.port else "")
    rabbitmq_vhost = rabbitmq_vhost or db_name_from_path(rabbit_dsn.path)
```

Use `clickhouse_dsn` as the primary clickhouse host list when present.

- [ ] **Step 4: Run Kuboard tests**

Run:

```bash
python3 templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
```

Expected: pass.

---

## Task 6: Evidence Analyzer Enrichment

**Files:**
- Modify `templates/workspace/skills/frontend-repro-investigator/scripts/har_analyzer.py`
- Modify `templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py`
- Modify `templates/workspace/skills/frontend-repro-investigator/scripts/console_analyzer.py`
- Modify `templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py`

- [ ] **Step 1: Add HAR enrichment tests**

Add to `test_har_analyzer.py`:

```python
    def test_detects_csp_mixed_content_and_source_map_failures(self):
        har = {"log": {"entries": [
            entry("https://shop.example.com/assets/app.js.map", status=404, body="not found"),
            entry("http://api.example.com/api/profile", status=0, body="mixed content blocked"),
            entry("https://shop.example.com/api/profile", status=200, response_headers={"Content-Security-Policy": "default-src 'self'"}, body=""),
        ]}}
        res = self.run_script(har)
        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        types = [item["type"] for item in payload["frontend_findings"]]
        self.assertIn("source_map_missing", types)
        self.assertIn("mixed_content_or_tls_block", types)
        self.assertIn("csp_present", types)
```

- [ ] **Step 2: Add console enrichment tests**

Add to `test_console_analyzer.py`:

```python
    def test_classifies_chunk_csp_and_mixed_content_console_errors(self):
        text = """
ChunkLoadError: Loading chunk 123 failed.
Refused to load the script 'https://cdn.example.com/app.js' because it violates Content Security Policy.
Mixed Content: The page at 'https://shop.example.com' was loaded over HTTPS, but requested an insecure resource 'http://api.example.com/api/profile'.
"""
        res = self.run_script(text)
        self.assertEqual(res.returncode, 0, res.stderr + res.stdout)
        payload = json.loads(res.stdout)
        types = [item["type"] for item in payload["frontend_findings"]]
        self.assertIn("chunk_load_error", types)
        self.assertIn("csp_violation", types)
        self.assertIn("mixed_content", types)
```

- [ ] **Step 3: Run failing evidence tests**

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py
```

Expected: fail because new finding types are not emitted.

- [ ] **Step 4: Implement HAR findings**

In `har_analyzer.py`, add helpers:

```python
def is_source_map(url: str) -> bool:
    return path_for(url).lower().endswith(".map")


def has_csp(entry: dict) -> bool:
    return bool(response_header(entry, "Content-Security-Policy"))


def is_mixed_content_candidate(url: str, status: int, snippet: str) -> bool:
    return url.startswith("http://") and (status == 0 or "mixed content" in snippet.lower())
```

Inside `analyze`, append findings:

```python
        if is_source_map(url) and status >= 400:
            frontend_findings.append({
                "type": "source_map_missing",
                "url": url,
                "status": status,
                "hint": "Source map is missing. Debugging stack traces may need matching build artifact or source map upload.",
            })
        if has_csp(entry):
            frontend_findings.append({
                "type": "csp_present",
                "url": url,
                "status": status,
                "hint": "CSP header is present. If browser console reports CSP violations, inspect script/connect-src directives.",
            })
        if is_mixed_content_candidate(url, status, item["response_snippet"]):
            frontend_findings.append({
                "type": "mixed_content_or_tls_block",
                "url": url,
                "status": status,
                "hint": "HTTPS page may be requesting HTTP API/static resource. Check environment API domain and TLS config.",
            })
```

- [ ] **Step 5: Implement console findings**

In `console_analyzer.py`, add before returning from `parse_text`:

```python
    lowered = text.lower()
    if "chunkloaderror" in lowered or "loading chunk" in lowered:
        frontend_findings.append({
            "type": "chunk_load_error",
            "hint": "Check frontend deploy version, stale index.html, CDN cache, and removed chunk files.",
        })
    if "content security policy" in lowered or "violates content security policy" in lowered:
        frontend_findings.append({
            "type": "csp_violation",
            "hint": "Check CSP script-src/connect-src directives and environment domains.",
        })
    if "mixed content" in lowered:
        frontend_findings.append({
            "type": "mixed_content",
            "hint": "HTTPS page requested HTTP resource. Check API domain scheme and TLS termination.",
        })
```

- [ ] **Step 6: Run evidence tests**

Run:

```bash
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py
```

Expected: pass.

---

## Task 7: Generated Workspace E2E Coverage

**Files:**
- Modify `internal/generator/generator_test.go`

- [ ] **Step 1: Add generated workspace artifact test**

Add to `internal/generator/generator_test.go`:

```go
func TestGenerate_FrontendReproScriptsIncludedAndTestsFiltered(t *testing.T) {
	cfg := loadCfg(t, "examples/three-tier-troubleshooter.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	root := filepath.Join(out, "templates/workspace-template/skills/frontend-repro-investigator/scripts")
	for _, rel := range []string{
		"har_analyzer.py",
		"console_analyzer.py",
		"browser_collect.mjs",
		"sentry_fetch.py",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("expected generated script %s: %v", rel, err)
		}
	}
	for _, rel := range []string{
		"test_har_analyzer.py",
		"test_console_analyzer.py",
		"test_browser_collect.py",
		"test_sentry_fetch.py",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Fatalf("repo-side test script should be filtered from generated workspace: %s err=%v", rel, err)
		}
	}
}
```

- [ ] **Step 2: Run failing E2E test**

Run:

```bash
go test ./internal/generator -run TestGenerate_FrontendReproScriptsIncludedAndTestsFiltered
```

Expected: fail until Task 3 and Task 4 scripts exist and the generator copies them correctly.

- [ ] **Step 3: Run generator tests**

Run:

```bash
go test ./internal/generator
```

Expected: pass.

---

## Task 8: Coverage Gate Ratchet

**Files:**
- Modify `scripts/check-go-coverage.sh`

- [ ] **Step 1: Run coverage and record current package values**

Run:

```bash
go test ./... -cover
./scripts/check-go-coverage.sh
```

Expected: pass and show current package percentages.

- [ ] **Step 2: Raise thresholds that improved through real tests**

If Task 1 and Task 2 tests improve package coverage, update only thresholds that are comfortably below observed values by at least 0.5 points. Example patch if values match the previous run:

```diff
-github.com/xiaolong/troubleshooter-studio/internal/analyzer 29
+github.com/xiaolong/troubleshooter-studio/internal/analyzer 30
-github.com/xiaolong/troubleshooter-studio/internal/generator 60
+github.com/xiaolong/troubleshooter-studio/internal/generator 62
```

Do not raise low-coverage packages unrelated to this work.

- [ ] **Step 3: Run coverage gate**

Run:

```bash
./scripts/check-go-coverage.sh
```

Expected: pass.

---

## Task 9: Documentation and Test Command Index

**Files:**
- Modify `CONTRIBUTING.md`
- Modify `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`

- [ ] **Step 1: Add consolidated repo-side test commands**

Ensure `CONTRIBUTING.md` includes:

```bash
python3 templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_browser_collect.py
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_sentry_fetch.py
go test ./internal/analyzer ./internal/generator
```

- [ ] **Step 2: Ensure skill workflow order is explicit**

In `SKILL.md.tmpl`, make the flow explicit:

```markdown
Evidence priority:

1. Use HAR when available.
2. Use browser collector when the user gives a reproducible URL and login state is not required or can be prepared manually.
3. Use console/Sentry analyzer when HAR is not available.
4. Use Sentry fetch when the user gives event id/project and a token is available.
5. Hand off by trace id first, then route candidate, then broad candidate service.
```

- [ ] **Step 3: Run docs-related tests**

Run:

```bash
go test ./internal/generator -run TestGenerate_FrontendReproScriptsIncludedAndTestsFiltered
```

Expected: pass.

---

## Task 10: Final Verification, Commit, and Push

**Files:**
- All files touched by Tasks 1-9.

- [ ] **Step 1: Format Go files**

Run:

```bash
gofmt -w internal/analyzer/types.go internal/analyzer/api_route_scan.go internal/analyzer/api_route_scan_test.go internal/analyzerpipe/pipeline.go internal/generator/generator.go internal/generator/funcs.go internal/generator/generator_test.go
```

Expected: exit 0.

- [ ] **Step 2: Run Python script tests**

Run:

```bash
python3 templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_browser_collect.py
python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_sentry_fetch.py
```

Expected: all pass. Remove any generated `__pycache__` directories after running:

```bash
find templates/workspace/skills -type d -name '__pycache__' -prune -exec rm -rf {} +
```

- [ ] **Step 3: Run Go verification**

Run:

```bash
go test ./... -race
./scripts/check-go-coverage.sh
```

Expected: both pass.

- [ ] **Step 4: Run web verification**

Run:

```bash
cd web && npm test -- --run
cd web && npm run build
```

Expected: both pass.

- [ ] **Step 5: Run audit and diff checks**

Run:

```bash
make audit
python3 - <<'PY'
import yaml
for path in ['.github/workflows/ci.yml', '.gitlab-ci.yml']:
    with open(path, encoding='utf-8') as f:
        yaml.safe_load(f)
    print(path, 'OK')
PY
git diff --check
git status --short
```

Expected: audit passes, YAML parses, no whitespace errors, and status only lists intended files.

- [ ] **Step 6: Commit**

Stage only intended files:

```bash
git add \
  internal/analyzer/types.go \
  internal/analyzer/api_route_scan.go \
  internal/analyzer/api_route_scan_test.go \
  internal/analyzerpipe/pipeline.go \
  internal/generator/generator.go \
  internal/generator/funcs.go \
  internal/generator/generator_test.go \
  templates/workspace/skills/routing/references/frontend-entry-map.yaml.tmpl \
  templates/workspace/skills/frontend-repro-investigator/scripts/browser_collect.mjs \
  templates/workspace/skills/frontend-repro-investigator/scripts/test_browser_collect.py \
  templates/workspace/skills/frontend-repro-investigator/scripts/sentry_fetch.py \
  templates/workspace/skills/frontend-repro-investigator/scripts/test_sentry_fetch.py \
  templates/workspace/skills/frontend-repro-investigator/scripts/har_analyzer.py \
  templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py \
  templates/workspace/skills/frontend-repro-investigator/scripts/console_analyzer.py \
  templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py \
  templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl \
  templates/workspace/skills/config-executor/scripts/kuboard_config.py \
  templates/workspace/skills/config-executor/scripts/test_kuboard_config.py \
  CONTRIBUTING.md \
  scripts/check-go-coverage.sh \
  docs/superpowers/plans/2026-07-03-next-optimization-wave.md
git commit -m "feat: improve frontend evidence handoff precision"
```

Expected: commit succeeds.

- [ ] **Step 7: Push**

Run:

```bash
git push
```

Expected: current branch pushes successfully.

---

## Self-Review

- Spec coverage: endpoint-to-service precision, optional browser artifact collection, Sentry/RUM event lookup, DSN parsing, evidence analyzer enrichment, coverage ratchet, generated workspace E2E, and final verification are all covered.
- Placeholder scan: the plan contains concrete file paths, commands, expected results, and code snippets for every implementation task.
- Type consistency: `APIRoute` is produced by analyzer, carried through `RepoAnalysis`, loaded into generator context, rendered into `frontend-entry-map.yaml`, and asserted by generator tests.
