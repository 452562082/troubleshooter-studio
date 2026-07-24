# Risk Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the highest-risk project drift points before larger feature work.

**Architecture:** Keep the existing two-layer product model intact: studio entrypoints share `internal/`, while generated robots remain template-driven. The first fix restores the documented HTTP server entrypoint by adding a thin `tshoot serve` CLI wrapper around the existing `api.NewRouter` and embedded web UI.

**Tech Stack:** Go 1.25, net/http, existing `api` and `internal/webui` packages, Vue/Vitest for frontend guard tests, existing Go package tests.

---

### Task 1: Restore `tshoot serve` HTTP Entrypoint

**Files:**
- Create: `cmd/tshoot/serve.go`
- Create: `cmd/tshoot/serve_test.go`
- Modify: `cmd/tshoot/main.go`
- Modify: `README.md`

- [ ] **Step 1: Write the failing test**

Create `cmd/tshoot/serve_test.go`:

```go
package main

import (
	"testing"
	"time"
)

func TestParseServeFlagsDefaults(t *testing.T) {
	opts, err := parseServeFlags(nil)
	if err != nil {
		t.Fatalf("parseServeFlags(nil): %v", err)
	}
	if opts.addr != "127.0.0.1:8080" {
		t.Fatalf("addr = %q, want 127.0.0.1:8080", opts.addr)
	}
	if opts.readHeaderTimeout != 5*time.Second {
		t.Fatalf("readHeaderTimeout = %v, want 5s", opts.readHeaderTimeout)
	}
}

func TestParseServeFlagsCustomAddr(t *testing.T) {
	opts, err := parseServeFlags([]string{"--addr", "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("parseServeFlags custom addr: %v", err)
	}
	if opts.addr != "127.0.0.1:0" {
		t.Fatalf("addr = %q, want 127.0.0.1:0", opts.addr)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./cmd/tshoot -run TestParseServeFlags
```

Expected: FAIL because `parseServeFlags` is undefined.

- [ ] **Step 3: Implement minimal serve command**

Create `cmd/tshoot/serve.go` with:

```go
package main

import (
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/xiaolong/troubleshooter-studio/api"
	"github.com/xiaolong/troubleshooter-studio/internal/webui"
)

type serveOptions struct {
	addr              string
	readHeaderTimeout time.Duration
}

func parseServeFlags(args []string) (serveOptions, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	opts := serveOptions{readHeaderTimeout: 5 * time.Second}
	fs.StringVar(&opts.addr, "addr", "127.0.0.1:8080", "listen address")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	return opts, nil
}

func runServe(args []string) error {
	opts, err := parseServeFlags(args)
	if err != nil {
		return err
	}
	srv := &api.Server{TemplateRoot: resolveTemplateDir()}
	httpSrv := &http.Server{
		Addr:              opts.addr,
		Handler:           api.NewRouter(srv, webui.Distribution()),
		ReadHeaderTimeout: opts.readHeaderTimeout,
	}
	fmt.Printf("tshoot serve listening on http://%s\n", opts.addr)
	return httpSrv.ListenAndServe()
}
```

Modify `cmd/tshoot/main.go` to route `serve`:

```go
case "serve":
	if err := runServe(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
```

Update `usage()` and README CLI command table to mention `serve`.

- [ ] **Step 4: Run focused tests**

Run:

```bash
go test ./cmd/tshoot -run TestParseServeFlags
go test ./api
```

Expected: PASS.

- [ ] **Step 5: Run broader verification**

Run:

```bash
go test ./...
npm test -- --run
```

Expected: both PASS.

### Task 2: Add Schema Drift Checklist Guard

**Files:**
- Create: `internal/config/schema_examples_test.go`
- Modify only if needed: `examples/*.yaml`

- [ ] **Step 1: Write failing/guard test**

Add a Go test that loads every tracked `examples/*.yaml` and calls `config.LoadFile` or `config.LoadFromBytes` plus `config.Validate`.

- [ ] **Step 2: Run test**

Run:

```bash
go test ./internal/config -run TestExamplesValidate
```

Expected: PASS if examples are already aligned; FAIL if a fixture drift exists.

- [ ] **Step 3: Fix examples only if the guard exposes real drift**

Do not change schema or code unless the example fixture is stale.

### Task 3: Add Frontend/Go Target Enum Guard

**Files:**
- Modify: `web/src/lib/constants.ts` or nearest existing target constants file
- Modify: `web/src/lib/yamlValidator.test.ts`
- Modify: `internal/config/validate.go` only if enum mismatch exists

- [ ] **Step 1: Locate frontend target source of truth**

Use `rg "openclaw|claude-code|cursor|codex" web/src`.

- [ ] **Step 2: Add test covering all Go-supported targets**

Ensure frontend validation accepts `openclaw`, `claude-code`, `cursor`, and `codex`.

- [ ] **Step 3: Run frontend tests**

Run:

```bash
npm test -- --run
```

Expected: PASS.

### Task 4: Strengthen MCP Builder Change Guard

**Files:**
- Modify: `internal/agent/install_native_mcp_common_test.go` or add a focused test beside existing MCP builder tests
- Modify only if needed: `internal/agent/self_test_openclaw_probes.go`

- [ ] **Step 1: Add test for required MCP key parity**

Build a representative config with nacos, one2all, grafana, jaeger, elk, data stores, lark, rabbitmq, and feishu_project. Assert:

- enabled MCP builders appear in `BuildMCPServers`
- rabbitmq does not appear because it is scheme B HTTP fallback
- feishu_project does not appear because it is true disabled
- nacos appears as local MCP

- [ ] **Step 2: Run focused tests**

Run:

```bash
go test ./internal/agent/ -run 'TestBuildMCPServers|TestSelfTestOpenclaw'
```

Expected: PASS.

### Task 5: Final Verification

**Files:**
- No additional file changes expected.

- [ ] **Step 1: Run required checks**

Run:

```bash
go test ./...
go test ./... -cover
npm test -- --run
```

Expected: all PASS; key package coverage remains above CONTRIBUTING thresholds.

- [ ] **Step 2: Review git diff**

Run:

```bash
git diff --stat
git diff --check
git status --short
```

Expected: no whitespace errors; only planned files changed.
