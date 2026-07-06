# Codex Bug Investigation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Codex-backed `开始排障` workflow to Bug 工单 so a selected Codex bot can run against a selected Bug and stream results back into Studio.

**Architecture:** Add a focused `internal/bughub` investigation layer for run persistence, Codex JSONL parsing, command construction, and process execution. Expose it through new Wails bindings in `cmd/tshoot-desktop`, then update the existing Bug 工单 bridge/page to start, cancel, list, and render investigation runs. Use `codex exec --json` as the stable automation boundary; do not automate Codex UI clicks.

**Tech Stack:** Go 1.25, Wails v2 events, Vue 3, TypeScript, Vitest, local JSON persistence under `~/.tshoot/bugs`.

---

## File Structure

- Create `internal/bughub/investigation.go`: run/event models, run store, active process manager interfaces.
- Create `internal/bughub/codex_events.go`: parse `codex exec --json` JSONL events into display events and final message/error.
- Create `internal/bughub/codex_runner.go`: build prompts, resolve command args, run/cancel Codex processes, append events.
- Create `internal/bughub/investigation_test.go`: store, parser, command builder, fake process tests.
- Create `cmd/tshoot-desktop/bindings_bug_investigation.go`: Wails bindings and event emitter adapter.
- Create `cmd/tshoot-desktop/bindings_bug_investigation_test.go`: fake `codex` executable integration tests.
- Modify `cmd/tshoot-desktop/main.go`: add investigation manager fields.
- Modify `web/src/lib/bridge/bugs.ts`: add investigation types and bridge methods.
- Modify `web/src/lib/bridge/bugs.test.ts`: desktop/browser bridge coverage.
- Modify `web/src/pages/BugWorkbenchPage.vue`: replace context-only panel with Codex launch/run panel.
- Modify `web/src/pages/BugWorkbenchPage.test.ts`: Codex/non-Codex UI behavior and run rendering.
- Run `make wails-gen` after adding Wails bindings, or update generated Wails files if this repo expects checked-in bindings.

## Task 1: Investigation Run Store And Models

**Files:**
- Create: `internal/bughub/investigation.go`
- Modify: `internal/bughub/investigation_test.go`

- [ ] **Step 1: Write failing store tests**

Add `internal/bughub/investigation_test.go`:

```go
package bughub

import (
	"testing"
	"time"
)

func TestInvestigationStoreCreateAppendAndList(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	run := InvestigationRun{
		ID:            "run-1",
		BugID:         "zentao-577",
		BotKey:        "/Users/me/.codex/agents/base.toml|codex",
		Status:        InvestigationRunning,
		StartedAt:     time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC),
		PromptPreview: "Investigate bug",
	}
	if err := store.Upsert(run); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.AppendEvent("run-1", InvestigationEvent{Type: "agent_message", Message: "checking logs"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := store.Finish("run-1", InvestigationSucceeded, "root cause", ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	runs, err := store.ListByBug("zentao-577")
	if err != nil {
		t.Fatalf("ListByBug: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs len = %d", len(runs))
	}
	got := runs[0]
	if got.Status != InvestigationSucceeded || got.FinalMessage != "root cause" {
		t.Fatalf("run = %+v", got)
	}
	if len(got.Events) != 1 || got.Events[0].Message != "checking logs" {
		t.Fatalf("events = %+v", got.Events)
	}
}

func TestInvestigationStoreActiveRunForBug(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	if err := store.Upsert(InvestigationRun{ID: "done", BugID: "b1", Status: InvestigationSucceeded}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(InvestigationRun{ID: "running", BugID: "b1", Status: InvestigationRunning}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.ActiveRunForBug("b1")
	if err != nil {
		t.Fatalf("ActiveRunForBug: %v", err)
	}
	if !ok || got.ID != "running" {
		t.Fatalf("active ok=%v run=%+v", ok, got)
	}
}
```

- [ ] **Step 2: Run store tests and verify red**

Run:

```bash
go test ./internal/bughub -run TestInvestigationStore -count=1
```

Expected: FAIL because `NewInvestigationStore`, `InvestigationRun`, and status constants are undefined.

- [ ] **Step 3: Implement run store**

Create `internal/bughub/investigation.go`:

```go
package bughub

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type InvestigationStatus string

const (
	InvestigationQueued    InvestigationStatus = "queued"
	InvestigationRunning   InvestigationStatus = "running"
	InvestigationSucceeded InvestigationStatus = "succeeded"
	InvestigationFailed    InvestigationStatus = "failed"
	InvestigationCancelled InvestigationStatus = "cancelled"
)

type InvestigationEvent struct {
	At      time.Time         `json:"at"`
	Type    string            `json:"type"`
	Message string            `json:"message"`
	Raw     map[string]any    `json:"raw,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
}

type InvestigationRun struct {
	ID            string               `json:"id"`
	BugID         string               `json:"bug_id"`
	BotKey        string               `json:"bot_key"`
	Status        InvestigationStatus  `json:"status"`
	StartedAt     time.Time            `json:"started_at"`
	FinishedAt    time.Time            `json:"finished_at,omitempty"`
	PromptPreview string               `json:"prompt_preview,omitempty"`
	Events        []InvestigationEvent `json:"events,omitempty"`
	FinalMessage  string               `json:"final_message,omitempty"`
	Error         string               `json:"error,omitempty"`
}

type InvestigationStore struct {
	root string
}

func NewInvestigationStore(root string) *InvestigationStore {
	return &InvestigationStore{root: root}
}

func (s *InvestigationStore) Path() string {
	return filepath.Join(s.root, "runs.json")
}

func (s *InvestigationStore) ListByBug(bugID string) ([]InvestigationRun, error) {
	items, err := s.readAll()
	if err != nil {
		return nil, err
	}
	out := make([]InvestigationRun, 0, len(items))
	for _, item := range items {
		if item.BugID == bugID {
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

func (s *InvestigationStore) ActiveRunForBug(bugID string) (InvestigationRun, bool, error) {
	items, err := s.ListByBug(bugID)
	if err != nil {
		return InvestigationRun{}, false, err
	}
	for _, item := range items {
		if item.Status == InvestigationQueued || item.Status == InvestigationRunning {
			return item, true, nil
		}
	}
	return InvestigationRun{}, false, nil
}

func (s *InvestigationStore) Upsert(run InvestigationRun) error {
	if strings.TrimSpace(run.ID) == "" {
		return errors.New("investigation run id is required")
	}
	if strings.TrimSpace(run.BugID) == "" {
		return errors.New("investigation bug id is required")
	}
	if run.Status == "" {
		run.Status = InvestigationQueued
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	items, err := s.readAll()
	if err != nil {
		return err
	}
	for i := range items {
		if items[i].ID == run.ID {
			items[i] = run
			return s.writeAll(items)
		}
	}
	items = append(items, run)
	return s.writeAll(items)
}

func (s *InvestigationStore) AppendEvent(runID string, event InvestigationEvent) error {
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	items, err := s.readAll()
	if err != nil {
		return err
	}
	for i := range items {
		if items[i].ID == runID {
			items[i].Events = append(items[i].Events, event)
			return s.writeAll(items)
		}
	}
	return os.ErrNotExist
}

func (s *InvestigationStore) Finish(runID string, status InvestigationStatus, finalMessage string, errorText string) error {
	items, err := s.readAll()
	if err != nil {
		return err
	}
	for i := range items {
		if items[i].ID == runID {
			items[i].Status = status
			items[i].FinishedAt = time.Now().UTC()
			items[i].FinalMessage = finalMessage
			items[i].Error = errorText
			return s.writeAll(items)
		}
	}
	return os.ErrNotExist
}

func (s *InvestigationStore) readAll() ([]InvestigationRun, error) {
	data, err := os.ReadFile(s.Path())
	if os.IsNotExist(err) {
		return []InvestigationRun{}, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return []InvestigationRun{}, nil
	}
	var items []InvestigationRun
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	if items == nil {
		return []InvestigationRun{}, nil
	}
	return items, nil
}

func (s *InvestigationStore) writeAll(items []InvestigationRun) error {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path(), append(data, '\n'), 0o600)
}
```

- [ ] **Step 4: Run store tests and verify green**

Run:

```bash
go test ./internal/bughub -run TestInvestigationStore -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/bughub/investigation.go internal/bughub/investigation_test.go
git commit -m "feat: add bug investigation run store"
```

## Task 2: Codex Event Parser And Prompt Builder

**Files:**
- Modify: `internal/bughub/investigation_test.go`
- Create: `internal/bughub/codex_events.go`
- Create: `internal/bughub/codex_runner.go`

- [ ] **Step 1: Write parser and prompt failing tests**

Append to `internal/bughub/investigation_test.go`:

```go
func TestParseCodexJSONLEvent(t *testing.T) {
	event, final, failed := ParseCodexJSONLEvent([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"root cause found"}}`))
	if event.Type != "agent_message" || event.Message != "root cause found" {
		t.Fatalf("event = %+v", event)
	}
	if final != "root cause found" || failed != "" {
		t.Fatalf("final=%q failed=%q", final, failed)
	}

	event, final, failed = ParseCodexJSONLEvent([]byte(`{"type":"turn.failed","error":{"message":"auth missing"}}`))
	if event.Type != "turn_failed" || failed != "auth missing" || final != "" {
		t.Fatalf("event=%+v final=%q failed=%q", event, final, failed)
	}

	event, final, failed = ParseCodexJSONLEvent([]byte(`not-json`))
	if event.Type != "raw" || event.Message != "not-json" || final != "" || failed != "" {
		t.Fatalf("malformed event=%+v final=%q failed=%q", event, final, failed)
	}
}

func TestBuildCodexInvestigationPromptIncludesBugAndBot(t *testing.T) {
	bug := Bug{ID: "zentao-577", Source: "zentao", SourceID: "577", Title: "搜索结果错误", Steps: "1. 搜索电影"}
	bot := BotRef{Key: "/tmp/base.toml|codex", SystemID: "base", Target: "codex", Path: "/tmp/base.toml"}
	prompt := BuildCodexInvestigationPrompt(bug, bot)
	for _, want := range []string{
		"请作为选定的 Codex 排障机器人开始排障",
		"搜索结果错误",
		"zentao:577",
		"target: codex",
		"不要修改代码",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
```

Add `strings` to the existing test imports.

- [ ] **Step 2: Run parser/prompt tests and verify red**

Run:

```bash
go test ./internal/bughub -run 'TestParseCodexJSONLEvent|TestBuildCodexInvestigationPrompt' -count=1
```

Expected: FAIL because parser and prompt functions are undefined.

- [ ] **Step 3: Implement parser**

Create `internal/bughub/codex_events.go`:

```go
package bughub

import (
	"encoding/json"
	"strings"
)

func ParseCodexJSONLEvent(line []byte) (InvestigationEvent, string, string) {
	rawLine := strings.TrimSpace(string(line))
	if rawLine == "" {
		return InvestigationEvent{Type: "raw", Message: ""}, "", ""
	}
	var payload map[string]any
	if err := json.Unmarshal(line, &payload); err != nil {
		return InvestigationEvent{Type: "raw", Message: rawLine}, "", ""
	}
	typ := stringFromAny(payload["type"])
	event := InvestigationEvent{Type: typ, Message: typ, Raw: payload}
	switch typ {
	case "thread.started":
		event.Type = "thread_started"
		event.Message = "Codex 线程已启动"
	case "turn.started":
		event.Type = "turn_started"
		event.Message = "开始排障"
	case "turn.completed":
		event.Type = "turn_completed"
		event.Message = "排障完成"
	case "turn.failed":
		event.Type = "turn_failed"
		msg := errorMessageFromPayload(payload)
		event.Message = msg
		return event, "", msg
	case "error":
		msg := errorMessageFromPayload(payload)
		event.Type = "error"
		event.Message = msg
		return event, "", msg
	case "item.started", "item.completed":
		item, _ := payload["item"].(map[string]any)
		itemType := stringFromAny(item["type"])
		switch itemType {
		case "agent_message":
			text := stringFromAny(item["text"])
			event.Type = "agent_message"
			event.Message = text
			return event, text, ""
		case "command_execution":
			event.Type = "command_execution"
			event.Message = stringFromAny(item["command"])
		case "mcp_tool_call":
			event.Type = "mcp_tool_call"
			event.Message = firstNonEmpty(stringFromAny(item["name"]), "MCP tool call")
		default:
			event.Type = firstNonEmpty(itemType, typ)
			event.Message = firstNonEmpty(stringFromAny(item["text"]), event.Message)
		}
	}
	return event, "", ""
}

func errorMessageFromPayload(payload map[string]any) string {
	if errObj, ok := payload["error"].(map[string]any); ok {
		return firstNonEmpty(stringFromAny(errObj["message"]), stringFromAny(errObj["code"]), "Codex run failed")
	}
	return firstNonEmpty(stringFromAny(payload["message"]), "Codex run failed")
}
```

- [ ] **Step 4: Implement prompt builder**

Create `internal/bughub/codex_runner.go` with only the prompt builder for this task:

```go
package bughub

import "strings"

func BuildCodexInvestigationPrompt(b Bug, bot BotRef) string {
	var sb strings.Builder
	sb.WriteString("请作为选定的 Codex 排障机器人开始排障。\n")
	sb.WriteString("目标：基于下面 Bug 工单上下文，完成只读根因分析，输出可执行结论和下一步建议。\n")
	sb.WriteString("约束：默认不要修改代码，不要执行破坏性命令；如必须写入或重启服务，先在结论中说明需要人工确认。\n\n")
	sb.WriteString(GenerateContext(b, bot))
	sb.WriteString("\n请按以下结构输出：\n")
	sb.WriteString("1. 现象复述\n2. 已验证事实\n3. 最可能根因\n4. 建议排查命令或证据\n5. 需要用户补充的信息\n")
	return sb.String()
}
```

- [ ] **Step 5: Run parser/prompt tests and verify green**

Run:

```bash
go test ./internal/bughub -run 'TestParseCodexJSONLEvent|TestBuildCodexInvestigationPrompt' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/bughub/codex_events.go internal/bughub/codex_runner.go internal/bughub/investigation_test.go
git commit -m "feat: parse codex investigation events"
```

## Task 3: Codex Command Builder And Process Manager

**Files:**
- Modify: `internal/bughub/codex_runner.go`
- Modify: `internal/bughub/investigation_test.go`

- [ ] **Step 1: Write failing command builder tests**

Append to `internal/bughub/investigation_test.go`:

```go
func TestBuildCodexExecCommandUsesSafeWorkspace(t *testing.T) {
	workspace := t.TempDir()
	cmd, err := BuildCodexExecCommand("codex", workspace, "hello")
	if err != nil {
		t.Fatalf("BuildCodexExecCommand: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "exec") || !strings.Contains(got, "--json") || !strings.Contains(got, "--cd "+workspace) {
		t.Fatalf("args = %q", got)
	}
	if !strings.Contains(got, "--sandbox workspace-write") {
		t.Fatalf("args missing sandbox: %q", got)
	}
	if !strings.Contains(got, "--skip-git-repo-check") {
		t.Fatalf("args missing git repo skip: %q", got)
	}
}

func TestBuildCodexExecCommandRejectsMissingWorkspace(t *testing.T) {
	_, err := BuildCodexExecCommand("codex", filepath.Join(t.TempDir(), "missing"), "hello")
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("err = %v", err)
	}
}
```

Add `os` and `path/filepath` to test imports.

- [ ] **Step 2: Run command builder tests and verify red**

Run:

```bash
go test ./internal/bughub -run TestBuildCodexExecCommand -count=1
```

Expected: FAIL because `BuildCodexExecCommand` is undefined.

- [ ] **Step 3: Implement command builder**

Append to `internal/bughub/codex_runner.go`:

```go
import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

func BuildCodexExecCommand(codexBin string, workspace string, prompt string) (*exec.Cmd, error) {
	if strings.TrimSpace(codexBin) == "" {
		codexBin = "codex"
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, errors.New("codex workspace is required")
	}
	if info, err := os.Stat(workspace); err != nil {
		return nil, fmt.Errorf("codex workspace: %w", err)
	} else if !info.IsDir() {
		return nil, errors.New("codex workspace must be a directory")
	}
	args := []string{
		"exec",
		"--json",
		"--sandbox", "workspace-write",
		"--skip-git-repo-check",
		"--cd", workspace,
		prompt,
	}
	cmd := exec.CommandContext(context.Background(), codexBin, args...)
	cmd.Dir = workspace
	return cmd, nil
}
```

If Go reports duplicate import blocks, merge the imports into one block:

```go
import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)
```

- [ ] **Step 4: Run command builder tests and verify green**

Run:

```bash
go test ./internal/bughub -run TestBuildCodexExecCommand -count=1
```

Expected: PASS.

- [ ] **Step 5: Add process manager tests**

Append to `internal/bughub/investigation_test.go`:

```go
func TestCodexInvestigatorRunsFakeCodex(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(workspace, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nprintf '%s\n' '{\"type\":\"thread.started\",\"thread_id\":\"t1\"}' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"final answer\"}}' '{\"type\":\"turn.completed\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "b|codex", Target: "codex", Path: workspace})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Status != InvestigationRunning {
		t.Fatalf("initial run = %+v", run)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != InvestigationSucceeded || waited.FinalMessage != "final answer" {
		t.Fatalf("waited = %+v", waited)
	}
}
```

Add `context` to test imports.

- [ ] **Step 6: Run process manager test and verify red**

Run:

```bash
go test ./internal/bughub -run TestCodexInvestigatorRunsFakeCodex -count=1
```

Expected: FAIL because `NewCodexInvestigator`, `Start`, and `Wait` are undefined.

- [ ] **Step 7: Implement process manager**

Append to `internal/bughub/codex_runner.go`:

```go
import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type CodexInvestigator struct {
	store    *InvestigationStore
	codexBin string
	mu       sync.Mutex
	active   map[string]*activeInvestigation
}

type activeInvestigation struct {
	done   chan struct{}
	cancel context.CancelFunc
	run    InvestigationRun
	err    error
}

func NewCodexInvestigator(store *InvestigationStore, codexBin string) *CodexInvestigator {
	return &CodexInvestigator{store: store, codexBin: codexBin, active: map[string]*activeInvestigation{}}
}

func (i *CodexInvestigator) Start(parent context.Context, bug Bug, bot BotRef) (InvestigationRun, error) {
	if strings.TrimSpace(bot.Target) != "codex" {
		return InvestigationRun{}, fmt.Errorf("direct investigation requires codex target")
	}
	if existing, ok, err := i.store.ActiveRunForBug(bug.ID); err != nil {
		return InvestigationRun{}, err
	} else if ok {
		return existing, nil
	}
	prompt := BuildCodexInvestigationPrompt(bug, bot)
	cmd, err := BuildCodexExecCommand(i.codexBin, bot.Path, prompt)
	if err != nil {
		return InvestigationRun{}, err
	}
	ctx, cancel := context.WithCancel(parent)
	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	cmd.Dir = bot.Path
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return InvestigationRun{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return InvestigationRun{}, err
	}
	run := InvestigationRun{
		ID:            "run-" + randomRunID(),
		BugID:         bug.ID,
		BotKey:        bot.Key,
		Status:        InvestigationRunning,
		StartedAt:     time.Now().UTC(),
		PromptPreview: promptPreview(prompt),
	}
	if err := i.store.Upsert(run); err != nil {
		cancel()
		return InvestigationRun{}, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = i.store.Finish(run.ID, InvestigationFailed, "", err.Error())
		return InvestigationRun{}, err
	}
	active := &activeInvestigation{done: make(chan struct{}), cancel: cancel, run: run}
	i.mu.Lock()
	i.active[run.ID] = active
	i.mu.Unlock()
	go i.collect(run.ID, cmd, stdout, stderr, active)
	return run, nil
}

func (i *CodexInvestigator) Cancel(runID string) error {
	i.mu.Lock()
	active := i.active[runID]
	i.mu.Unlock()
	if active == nil {
		return os.ErrNotExist
	}
	active.cancel()
	<-active.done
	return nil
}

func (i *CodexInvestigator) Wait(runID string) (InvestigationRun, error) {
	i.mu.Lock()
	active := i.active[runID]
	i.mu.Unlock()
	if active == nil {
		return InvestigationRun{}, os.ErrNotExist
	}
	<-active.done
	runs, err := i.store.ListByBug(active.run.BugID)
	if err != nil {
		return InvestigationRun{}, err
	}
	for _, run := range runs {
		if run.ID == runID {
			return run, active.err
		}
	}
	return InvestigationRun{}, os.ErrNotExist
}

func (i *CodexInvestigator) collect(runID string, cmd *exec.Cmd, stdout io.Reader, stderr io.Reader, active *activeInvestigation) {
	defer func() {
		i.mu.Lock()
		delete(i.active, runID)
		i.mu.Unlock()
		close(active.done)
	}()
	var finalMessage string
	var failure string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		event, final, failed := ParseCodexJSONLEvent(scanner.Bytes())
		if strings.TrimSpace(event.Message) != "" {
			_ = i.store.AppendEvent(runID, event)
		}
		if final != "" {
			finalMessage = final
		}
		if failed != "" {
			failure = failed
		}
	}
	stderrBytes, _ := io.ReadAll(stderr)
	waitErr := cmd.Wait()
	if waitErr != nil && failure == "" {
		failure = strings.TrimSpace(string(stderrBytes))
		if failure == "" {
			failure = waitErr.Error()
		}
	}
	if failure != "" {
		active.err = errors.New(failure)
		_ = i.store.Finish(runID, InvestigationFailed, finalMessage, failure)
		return
	}
	_ = i.store.Finish(runID, InvestigationSucceeded, finalMessage, "")
}

func randomRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprint(time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func promptPreview(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if len(prompt) <= 240 {
		return prompt
	}
	return prompt[:240]
}
```

Merge imports to include:

```go
import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)
```

- [ ] **Step 8: Run process manager tests and verify green**

Run:

```bash
go test ./internal/bughub -run 'TestBuildCodexExecCommand|TestCodexInvestigatorRunsFakeCodex' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/bughub/codex_runner.go internal/bughub/investigation_test.go
git commit -m "feat: run codex bug investigations"
```

## Task 4: Wails Bindings And Event Emission

**Files:**
- Create: `cmd/tshoot-desktop/bindings_bug_investigation.go`
- Create: `cmd/tshoot-desktop/bindings_bug_investigation_test.go`
- Modify: `cmd/tshoot-desktop/main.go`

- [ ] **Step 1: Write failing binding tests**

Create `cmd/tshoot-desktop/bindings_bug_investigation_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func TestStartBugInvestigationRunsCodexBot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nprintf '%s\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"done\"}}' '{\"type\":\"turn.completed\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-577", Source: "zentao", SourceID: "577", Title: "Bug"}); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	result, err := app.StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-577",
		Bot: bughub.BotRef{Key: repo + "|codex", Target: "codex", Path: repo, SystemID: "base"},
	})
	if err != nil {
		t.Fatalf("StartBugInvestigation: %v", err)
	}
	if result.Status != bughub.InvestigationRunning {
		t.Fatalf("result = %+v", result)
	}
}

func TestStartBugInvestigationRejectsNonCodexBot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-577", Source: "zentao", SourceID: "577", Title: "Bug"}); err != nil {
		t.Fatal(err)
	}
	_, err := (&App{}).StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-577",
		Bot: bughub.BotRef{Key: "base|cursor", Target: "cursor", Path: "/tmp/base"},
	})
	if err == nil {
		t.Fatal("StartBugInvestigation accepted non-codex bot")
	}
}
```

- [ ] **Step 2: Run binding tests and verify red**

Run:

```bash
go test ./cmd/tshoot-desktop -run TestStartBugInvestigation -count=1
```

Expected: FAIL because `BugInvestigationInput` and `StartBugInvestigation` are undefined.

- [ ] **Step 3: Add manager fields**

Modify `cmd/tshoot-desktop/main.go` `App` struct:

```go
	bugInvestigationMu sync.Mutex
	bugInvestigator    *bughub.CodexInvestigator
```

Add import:

```go
"github.com/xiaolong/troubleshooter-studio/internal/bughub"
```

If `main.go` already imports `bughub` after this work, reuse that import.

- [ ] **Step 4: Implement bindings**

Create `cmd/tshoot-desktop/bindings_bug_investigation.go`:

```go
package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type BugInvestigationInput struct {
	BugID string        `json:"bug_id"`
	Bot   bughub.BotRef `json:"bot"`
}

type BugInvestigationCancelInput struct {
	RunID string `json:"run_id"`
}

func (a *App) StartBugInvestigation(input BugInvestigationInput) (bughub.InvestigationRun, error) {
	bug, ok, err := bugStore().Get(input.BugID)
	if err != nil {
		return bughub.InvestigationRun{}, err
	}
	if !ok {
		return bughub.InvestigationRun{}, os.ErrNotExist
	}
	if strings.TrimSpace(input.Bot.Target) != "codex" {
		return bughub.InvestigationRun{}, errors.New("当前只支持 Codex 机器人直接排障")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return bughub.InvestigationRun{}, errors.New("未检测到 codex CLI")
	}
	return a.codexInvestigator().Start(a.ctx, bug, input.Bot)
}

func (a *App) CancelBugInvestigation(input BugInvestigationCancelInput) error {
	if strings.TrimSpace(input.RunID) == "" {
		return errors.New("run id is required")
	}
	return a.codexInvestigator().Cancel(input.RunID)
}

func (a *App) ListBugInvestigationRuns(bugID string) ([]bughub.InvestigationRun, error) {
	return bugInvestigationStore().ListByBug(bugID)
}

func (a *App) codexInvestigator() *bughub.CodexInvestigator {
	a.bugInvestigationMu.Lock()
	defer a.bugInvestigationMu.Unlock()
	if a.bugInvestigator == nil {
		a.bugInvestigator = bughub.NewCodexInvestigator(bugInvestigationStore(), "codex")
	}
	return a.bugInvestigator
}

func bugInvestigationStore() *bughub.InvestigationStore {
	return bughub.NewInvestigationStore(bughub.DefaultRoot())
}
```

- [ ] **Step 5: Run binding tests and verify green**

Run:

```bash
go test ./cmd/tshoot-desktop -run TestStartBugInvestigation -count=1
```

Expected: PASS.

- [ ] **Step 6: Add Wails event emission hook**

Modify `internal/bughub.CodexInvestigator` to accept an optional event callback:

```go
type InvestigationEventSink func(run InvestigationRun, event InvestigationEvent)

func (i *CodexInvestigator) SetEventSink(sink InvestigationEventSink) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.eventSink = sink
}
```

Add `eventSink InvestigationEventSink` to `CodexInvestigator`, and call it after each successful `AppendEvent`.

Modify `cmd/tshoot-desktop/bindings_bug_investigation.go`:

```go
import wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
```

Inside `codexInvestigator()` after construction:

```go
a.bugInvestigator.SetEventSink(func(run bughub.InvestigationRun, event bughub.InvestigationEvent) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "bug-investigation:event", map[string]any{
			"run":   run,
			"event": event,
		})
	}
})
```

- [ ] **Step 7: Run package tests**

Run:

```bash
go test ./internal/bughub ./cmd/tshoot-desktop -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/bughub/codex_runner.go cmd/tshoot-desktop/main.go cmd/tshoot-desktop/bindings_bug_investigation.go cmd/tshoot-desktop/bindings_bug_investigation_test.go
git commit -m "feat: expose codex bug investigation bindings"
```

## Task 5: Frontend Bridge

**Files:**
- Modify: `web/src/lib/bridge/bugs.ts`
- Modify: `web/src/lib/bridge/bugs.test.ts`

- [ ] **Step 1: Write failing bridge tests**

Append to `web/src/lib/bridge/bugs.test.ts`:

```ts
import {
  cancelBugInvestigation,
  listBugInvestigationRuns,
  startBugInvestigation,
} from './bugs'

it('forwards startBugInvestigation to Wails in desktop mode', async () => {
  const spy = vi.fn().mockResolvedValue({ id: 'run-1', status: 'running' })
  ;(window as any).go = { main: { App: { StartBugInvestigation: spy } } }
  const input = { bug_id: 'zentao-577', bot: { key: 'base|codex', target: 'codex', path: '/repo', system_id: 'base' } }

  const result = await startBugInvestigation(input)

  expect(spy).toHaveBeenCalledWith(input)
  expect(result.status).toBe('running')
})

it('forwards cancelBugInvestigation to Wails in desktop mode', async () => {
  const spy = vi.fn().mockResolvedValue(undefined)
  ;(window as any).go = { main: { App: { CancelBugInvestigation: spy } } }

  await cancelBugInvestigation({ run_id: 'run-1' })

  expect(spy).toHaveBeenCalledWith({ run_id: 'run-1' })
})

it('returns [] for listBugInvestigationRuns in browser mode', async () => {
  delete (window as any).go
  await expect(listBugInvestigationRuns('zentao-577')).resolves.toEqual([])
})
```

If duplicate imports become invalid, merge the new imported names into the existing import block.

- [ ] **Step 2: Run bridge tests and verify red**

Run:

```bash
cd web
npm test -- src/lib/bridge/bugs.test.ts
```

Expected: FAIL because bridge functions are undefined.

- [ ] **Step 3: Implement bridge types and methods**

Modify `web/src/lib/bridge/bugs.ts`:

```ts
export type InvestigationStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'

export interface InvestigationEvent {
  at?: string
  type: string
  message: string
  raw?: Record<string, unknown>
  meta?: Record<string, string>
}

export interface InvestigationRun {
  id: string
  bug_id: string
  bot_key: string
  status: InvestigationStatus
  started_at?: string
  finished_at?: string
  prompt_preview?: string
  events?: InvestigationEvent[]
  final_message?: string
  error?: string
}

export interface BugInvestigationInput {
  bug_id: string
  bot: BotRef
}

export interface BugInvestigationCancelInput {
  run_id: string
}

export async function startBugInvestigation(input: BugInvestigationInput): Promise<InvestigationRun> {
  if (!isDesktop()) throw new Error('启动排障只在桌面 app 可用')
  return normalizeInvestigationRun(await desktopApp.StartBugInvestigation(input))
}

export async function cancelBugInvestigation(input: BugInvestigationCancelInput): Promise<void> {
  if (!isDesktop()) throw new Error('停止排障只在桌面 app 可用')
  await desktopApp.CancelBugInvestigation(input)
}

export async function listBugInvestigationRuns(bugID: string): Promise<InvestigationRun[]> {
  if (!isDesktop()) return []
  const r = await desktopApp.ListBugInvestigationRuns(bugID)
  return Array.isArray(r) ? r.map(normalizeInvestigationRun) : []
}

function normalizeInvestigationRun(raw: any): InvestigationRun {
  return {
    id: String(raw?.id || ''),
    bug_id: String(raw?.bug_id || ''),
    bot_key: String(raw?.bot_key || ''),
    status: raw?.status || 'queued',
    started_at: raw?.started_at,
    finished_at: raw?.finished_at,
    prompt_preview: raw?.prompt_preview,
    events: Array.isArray(raw?.events) ? raw.events : [],
    final_message: raw?.final_message || '',
    error: raw?.error || '',
  }
}
```

- [ ] **Step 4: Run bridge tests and verify green**

Run:

```bash
cd web
npm test -- src/lib/bridge/bugs.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/bridge/bugs.ts web/src/lib/bridge/bugs.test.ts
git commit -m "feat: add bug investigation bridge"
```

## Task 6: Bug Workbench UI Launch Panel

**Files:**
- Modify: `web/src/pages/BugWorkbenchPage.vue`
- Modify: `web/src/pages/BugWorkbenchPage.test.ts`

- [ ] **Step 1: Write failing component tests**

Modify `web/src/pages/BugWorkbenchPage.test.ts` mock import list to include:

```ts
  cancelBugInvestigation: vi.fn(),
  listBugInvestigationRuns: vi.fn().mockResolvedValue([]),
  startBugInvestigation: vi.fn(),
```

Add imported functions:

```ts
import { listBugs, matchBugBots, startBugInvestigation, listBugInvestigationRuns } from '../lib/bridge'
```

Append tests:

```ts
it('shows start investigation for codex bot', async () => {
  vi.mocked(listBugs).mockResolvedValue([
    { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
  ])
  vi.mocked(matchBugBots).mockResolvedValue([
    { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
  ])
  const wrapper = mount(BugWorkbenchPage)
  await new Promise(resolve => setTimeout(resolve, 0))
  await new Promise(resolve => setTimeout(resolve, 0))

  expect(wrapper.text()).toContain('开始排障')
})

it('starts codex investigation from selected bug and bot', async () => {
  vi.mocked(listBugs).mockResolvedValue([
    { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
  ])
  vi.mocked(matchBugBots).mockResolvedValue([
    { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
  ])
  vi.mocked(startBugInvestigation).mockResolvedValue({
    id: 'run-1',
    bug_id: 'zentao-577',
    bot_key: 'base|codex',
    status: 'running',
    events: [],
  })
  const wrapper = mount(BugWorkbenchPage)
  await new Promise(resolve => setTimeout(resolve, 0))
  await new Promise(resolve => setTimeout(resolve, 0))

  const button = wrapper.findAll('button').find(b => b.text() === '开始排障')
  expect(button).toBeTruthy()
  await button!.trigger('click')

  expect(startBugInvestigation).toHaveBeenCalledWith({
    bug_id: 'zentao-577',
    bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' },
  })
})

it('renders final investigation output', async () => {
  vi.mocked(listBugs).mockResolvedValue([
    { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
  ])
  vi.mocked(matchBugBots).mockResolvedValue([
    { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
  ])
  vi.mocked(listBugInvestigationRuns).mockResolvedValue([
    { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'succeeded', final_message: '缓存配置错误', events: [] },
  ])

  const wrapper = mount(BugWorkbenchPage)
  await new Promise(resolve => setTimeout(resolve, 0))
  await new Promise(resolve => setTimeout(resolve, 0))

  expect(wrapper.text()).toContain('缓存配置错误')
})
```

- [ ] **Step 2: Run component tests and verify red**

Run:

```bash
cd web
npm test -- src/pages/BugWorkbenchPage.test.ts
```

Expected: FAIL because UI does not call start/list investigation methods.

- [ ] **Step 3: Implement UI state and actions**

Modify `web/src/pages/BugWorkbenchPage.vue` script imports:

```ts
  cancelBugInvestigation,
  listBugInvestigationRuns,
  startBugInvestigation,
  type InvestigationRun,
```

Add refs:

```ts
const investigationRuns = ref<InvestigationRun[]>([])
const investigationStarting = ref(false)
const investigationCancelling = ref(false)
```

Add computed:

```ts
const selectedRun = computed(() => investigationRuns.value[0])
const selectedBotIsCodex = computed(() => selectedBot.value?.target === 'codex')
const investigationOutput = computed(() => {
  const run = selectedRun.value
  if (!run) return contextText.value
  if (run.final_message) return run.final_message
  const lines = (run.events || []).map(e => e.message).filter(Boolean)
  return lines.join('\n')
})
```

Update `watch(selectedBug, ...)` to call:

```ts
await loadInvestigationRuns(bug.id)
```

Add functions:

```ts
async function loadInvestigationRuns(bugID: string) {
  try {
    investigationRuns.value = await listBugInvestigationRuns(bugID)
  } catch (e) {
    investigationRuns.value = []
    toastError('读取排障运行记录', e)
  }
}

async function startInvestigation() {
  const bug = selectedBug.value
  const bot = selectedBot.value
  if (!bug || !bot) {
    toast.error('请先选择 Bug 和机器人')
    return
  }
  if (bot.target !== 'codex') {
    toast.error('当前只支持 Codex 机器人直接排障')
    return
  }
  investigationStarting.value = true
  try {
    const run = await startBugInvestigation({ bug_id: bug.id, bot })
    investigationRuns.value = [run, ...investigationRuns.value.filter(r => r.id !== run.id)]
    toast.success('已启动 Codex 排障')
  } catch (e) {
    toastError('启动排障', e)
  } finally {
    investigationStarting.value = false
  }
}

async function cancelInvestigation() {
  const run = selectedRun.value
  if (!run) return
  investigationCancelling.value = true
  try {
    await cancelBugInvestigation({ run_id: run.id })
    if (selectedBug.value) await loadInvestigationRuns(selectedBug.value.id)
    toast.success('已停止排障')
  } catch (e) {
    toastError('停止排障', e)
  } finally {
    investigationCancelling.value = false
  }
}

async function copyInvestigationOutput() {
  if (!investigationOutput.value) return
  await copyToClipboard(investigationOutput.value)
  toast.success('已复制')
}
```

Change right-panel buttons:

```vue
<button
  class="btn primary"
  type="button"
  :disabled="!selectedBug || !selectedBotKey || !selectedBotIsCodex || investigationStarting"
  @click="startInvestigation"
>
  {{ investigationStarting ? '启动中...' : '开始排障' }}
</button>
<button
  class="btn"
  type="button"
  :disabled="!selectedRun || selectedRun.status !== 'running' || investigationCancelling"
  @click="cancelInvestigation"
>
  停止
</button>
<button class="btn" type="button" :disabled="!investigationOutput" @click="copyInvestigationOutput">复制</button>
```

Change textarea binding:

```vue
<textarea
  :value="investigationOutput"
  class="context-preview"
  readonly
  placeholder="开始排障后在这里显示过程和结论"
></textarea>
```

Add unsupported text under bot actions:

```vue
<p v-if="selectedBot && !selectedBotIsCodex" class="muted direct-launch-note">
  当前只支持 Codex 机器人直接排障。
</p>
```

- [ ] **Step 4: Run component tests and verify green**

Run:

```bash
cd web
npm test -- src/pages/BugWorkbenchPage.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/BugWorkbenchPage.vue web/src/pages/BugWorkbenchPage.test.ts
git commit -m "feat: launch codex investigations from bug workbench"
```

## Task 7: Generated Bindings, Build, And Full Verification

**Files:**
- Modify generated Wails files if required: `web/wailsjs/go/main/App.d.ts`, `web/wailsjs/go/main/App.js`, `web/wailsjs/go/models.ts`
- Modify build output if checked in by the current workflow: `web/dist/**`

- [ ] **Step 1: Regenerate Wails bindings**

Run:

```bash
make wails-gen
```

Expected: generated `web/wailsjs/go/main/App.*` includes `StartBugInvestigation`, `CancelBugInvestigation`, and `ListBugInvestigationRuns`.

- [ ] **Step 2: Run frontend tests**

Run:

```bash
cd web
npm test
```

Expected: all Vitest tests pass.

- [ ] **Step 3: Build frontend assets**

Run:

```bash
cd web
npm run build
```

Expected: production build succeeds and `web/dist/assets/BugWorkbenchPage-*.js` no longer contains only the old context-generation workflow.

- [ ] **Step 4: Run Go tests**

Run:

```bash
go test ./...
```

Expected: all Go tests pass.

- [ ] **Step 5: Manual smoke test**

Run the desktop app, open Bug 工单, select a synced Bug and a Codex bot.

Expected:

- `开始排障` is enabled for `codex` bots.
- Clicking it starts a run.
- Right panel shows progress/final output.
- `停止` cancels a long-running run.
- Non-Codex bots show unsupported direct-launch message.

- [ ] **Step 6: Commit final generated assets**

```bash
git status --short
git add web/wailsjs/go/main/App.d.ts web/wailsjs/go/main/App.js web/wailsjs/go/models.ts
git add web/dist
git status --short
git commit -m "feat: complete codex bug investigation launch"
```

Before committing, inspect `git status --short`. Stage only generated files from this plan and remove unrelated files from the index. Do not use `git add -A`.

---

## Self-Review

Spec coverage:

- Codex-only direct launch: Tasks 3, 4, 6.
- No UI automation: command uses `codex exec --json`, no browser/UI control.
- Run status, events, final answer: Tasks 1, 2, 4, 6.
- Missing CLI and non-Codex errors: Tasks 4 and 6.
- Persistence under `~/.tshoot/bugs/runs.json`: Task 1.
- Conservative sandbox: Task 3.
- Tests across backend/frontend/manual: Tasks 1-7.

Placeholder scan:

- No `TBD` or `TODO`.
- The only `placeholder` match is the user-facing textarea placeholder string.
- Deferred decisions from the spec are not implemented in this plan.

Type consistency:

- Backend run type: `InvestigationRun`.
- Frontend run type: `InvestigationRun`.
- Binding methods: `StartBugInvestigation`, `CancelBugInvestigation`, `ListBugInvestigationRuns`.
- Event name: `bug-investigation:event`.
