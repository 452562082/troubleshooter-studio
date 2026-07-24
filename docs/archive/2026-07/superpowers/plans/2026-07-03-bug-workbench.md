# Bug Workbench Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a desktop `Bug 工单` page that can store/import bugs, pull assigned bugs from Zentao, select an installed troubleshooting bot, and generate a deterministic troubleshooting context.

**Architecture:** Add a Studio-side `internal/bughub` package for normalized bug records, local JSON persistence, Zentao normalization, bot matching, and context generation. Expose it through Wails bindings in `cmd/tshoot-desktop/bindings_bug.go`, then add a Vue page and bridge module. Keep generated bot workspaces unchanged.

**Tech Stack:** Go 1.25, Wails v2 bindings, Vue 3, TypeScript, Vitest, local JSON files under `~/.tshoot/bugs`.

---

## File Structure

- Create `internal/bughub/types.go`: platform-neutral bug and bot match models.
- Create `internal/bughub/store.go`: local JSON store rooted at `~/.tshoot/bugs`.
- Create `internal/bughub/store_test.go`: store write/read/upsert behavior.
- Create `internal/bughub/context.go`: deterministic markdown context generation.
- Create `internal/bughub/context_test.go`: context generation tests.
- Create `internal/bughub/match.go`: installed-bot scoring helper.
- Create `internal/bughub/match_test.go`: match scoring tests.
- Create `internal/bughub/zentao.go`: Zentao response normalization and pull client.
- Create `internal/bughub/zentao_test.go`: fake-server API/normalization tests.
- Create `cmd/tshoot-desktop/bindings_bug.go`: Wails bindings used by the page.
- Modify `web/src/lib/bridge.ts`: re-export bug bridge module.
- Create `web/src/lib/bridge/bugs.ts`: frontend bridge/types.
- Modify `web/src/App.vue`: sidebar `Bug 工单` entry.
- Modify `web/src/router/index.ts`: `/bugs` route.
- Create `web/src/pages/BugWorkbenchPage.vue`: inbox/detail/bot selection/context UI.
- Create `web/src/lib/bridge/bugs.test.ts`: browser/desktop bridge behavior.

## Task 1: Backend Bug Models And Store

**Files:**
- Create: `internal/bughub/types.go`
- Create: `internal/bughub/store.go`
- Create: `internal/bughub/store_test.go`

- [ ] **Step 1: Write failing store tests**

```go
package bughub

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreUpsertAndList(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

	first := Bug{
		ID:        "zentao-1842",
		Source:    "zentao",
		SourceID:  "1842",
		Title:     "支付页提交后 500",
		Status:    "active",
		Severity:  "P1",
		Env:       "prod",
		UpdatedAt: now,
	}
	if err := store.Upsert(first); err != nil {
		t.Fatalf("upsert first: %v", err)
	}

	first.Title = "支付页提交后 500,用户无法完成付款"
	if err := store.Upsert(first); err != nil {
		t.Fatalf("upsert updated: %v", err)
	}

	got, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Title != first.Title {
		t.Fatalf("title = %q, want %q", got[0].Title, first.Title)
	}
	if got[0].UpdatedAt.IsZero() {
		t.Fatal("updated_at was not preserved")
	}
}

func TestStoreRejectsEmptyID(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Upsert(Bug{Title: "missing id"}); err == nil {
		t.Fatal("Upsert accepted empty ID")
	}
}

func TestStorePathIsUnderRoot(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if got := store.Path(); got != filepath.Join(root, "bugs.json") {
		t.Fatalf("Path = %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify RED**

Run:

```bash
go test ./internal/bughub -run TestStore -count=1
```

Expected: build fails because `internal/bughub` does not exist or `NewStore`/`Bug` is undefined.

- [ ] **Step 3: Implement models and store**

```go
// internal/bughub/types.go
package bughub

import "time"

type Attachment struct {
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
	RemoteURL string `json:"remote_url,omitempty"`
}

type Bug struct {
	ID           string       `json:"id"`
	Source       string       `json:"source"`
	SourceID     string       `json:"source_id,omitempty"`
	Title        string       `json:"title"`
	Description  string       `json:"description,omitempty"`
	Steps        string       `json:"steps,omitempty"`
	Expected     string       `json:"expected,omitempty"`
	Actual       string       `json:"actual,omitempty"`
	Status       string       `json:"status,omitempty"`
	Severity     string       `json:"severity,omitempty"`
	Priority     string       `json:"priority,omitempty"`
	Assignee     string       `json:"assignee,omitempty"`
	Reporter     string       `json:"reporter,omitempty"`
	CreatedAt    time.Time    `json:"created_at,omitempty"`
	UpdatedAt    time.Time    `json:"updated_at,omitempty"`
	Env          string       `json:"env,omitempty"`
	SystemID     string       `json:"system_id,omitempty"`
	FrontendRepo string       `json:"frontend_repo,omitempty"`
	ServiceHints []string     `json:"service_hints,omitempty"`
	FrontendURL  string       `json:"frontend_url,omitempty"`
	APIPaths     []string     `json:"api_paths,omitempty"`
	TraceIDs     []string     `json:"trace_ids,omitempty"`
	RequestIDs   []string     `json:"request_ids,omitempty"`
	Attachments  []Attachment `json:"attachments,omitempty"`
	SelectedBotKey string     `json:"selected_bot_key,omitempty"`
	LastContext    string     `json:"last_context,omitempty"`
	LastContextAt  time.Time  `json:"last_context_at,omitempty"`
	RawPreview     string     `json:"raw_preview,omitempty"`
}

type BotRef struct {
	Key      string `json:"key"`
	SystemID string `json:"system_id"`
	Target   string `json:"target"`
	Path     string `json:"path"`
	Name     string `json:"name,omitempty"`
	Env      string `json:"env,omitempty"`
}

type BotMatch struct {
	Bot     BotRef   `json:"bot"`
	Score   int      `json:"score"`
	Reasons []string `json:"reasons"`
}
```

```go
// internal/bughub/store.go
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

type Store struct {
	root string
}

func NewStore(root string) *Store {
	return &Store{root: root}
}

func (s *Store) Path() string {
	return filepath.Join(s.root, "bugs.json")
}

func (s *Store) List() ([]Bug, error) {
	items, err := s.readAll()
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items, nil
}

func (s *Store) Upsert(b Bug) error {
	if strings.TrimSpace(b.ID) == "" {
		return errors.New("bug id is required")
	}
	if b.UpdatedAt.IsZero() {
		b.UpdatedAt = time.Now().UTC()
	}
	items, err := s.readAll()
	if err != nil {
		return err
	}
	replaced := false
	for i := range items {
		if items[i].ID == b.ID {
			items[i] = b
			replaced = true
			break
		}
	}
	if !replaced {
		items = append(items, b)
	}
	return s.writeAll(items)
}

func (s *Store) readAll() ([]Bug, error) {
	data, err := os.ReadFile(s.Path())
	if os.IsNotExist(err) {
		return []Bug{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return []Bug{}, nil
	}
	var items []Bug
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) writeAll(items []Bug) error {
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

- [ ] **Step 4: Run store tests**

Run:

```bash
go test ./internal/bughub -run TestStore -count=1
```

Expected: PASS.

## Task 2: Context Generation And Bot Matching

**Files:**
- Create: `internal/bughub/context.go`
- Create: `internal/bughub/context_test.go`
- Create: `internal/bughub/match.go`
- Create: `internal/bughub/match_test.go`

- [ ] **Step 1: Write failing tests**

```go
package bughub

import (
	"strings"
	"testing"
)

func TestGenerateContextIncludesBugAndBot(t *testing.T) {
	bug := Bug{
		Source: "zentao", SourceID: "1842", Title: "支付页提交后 500",
		Env: "prod", FrontendURL: "https://mall.example.com/checkout",
		Steps: "1. 打开支付页\n2. 点击提交",
		APIPaths: []string{"/api/pay/submit"},
		TraceIDs: []string{"trace-1"},
		Attachments: []Attachment{{Name: "network.har", LocalPath: "/tmp/network.har"}},
	}
	bot := BotRef{Key: "/bots/shop|openclaw", SystemID: "shop", Target: "openclaw", Path: "/bots/shop"}

	ctx := GenerateContext(bug, bot)

	for _, want := range []string{
		"# Bug 排障上下文",
		"zentao:1842",
		"支付页提交后 500",
		"prod",
		"/api/pay/submit",
		"trace-1",
		"network.har",
		"shop",
		"openclaw",
	} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("context missing %q:\n%s", want, ctx)
		}
	}
}

func TestMatchBotsScoresSystemAndEnv(t *testing.T) {
	bug := Bug{SystemID: "shop", Env: "prod", FrontendRepo: "mall-web", ServiceHints: []string{"pay-service"}}
	bots := []BotRef{
		{Key: "shop-prod", SystemID: "shop", Env: "prod", Name: "shop-prod-troubleshooter"},
		{Key: "crm", SystemID: "crm", Env: "test", Name: "crm-troubleshooter"},
	}

	matches := MatchBots(bug, bots)

	if len(matches) != 2 {
		t.Fatalf("matches len = %d", len(matches))
	}
	if matches[0].Bot.Key != "shop-prod" {
		t.Fatalf("top match = %s", matches[0].Bot.Key)
	}
	if matches[0].Score <= matches[1].Score {
		t.Fatalf("top score %d <= second %d", matches[0].Score, matches[1].Score)
	}
	if len(matches[0].Reasons) == 0 {
		t.Fatal("top match has no reasons")
	}
}
```

- [ ] **Step 2: Run tests to verify RED**

Run:

```bash
go test ./internal/bughub -run 'TestGenerateContext|TestMatchBots' -count=1
```

Expected: FAIL with undefined `GenerateContext` and `MatchBots`.

- [ ] **Step 3: Implement context and matching**

```go
// internal/bughub/context.go
package bughub

import (
	"fmt"
	"strings"
)

func GenerateContext(b Bug, bot BotRef) string {
	var sb strings.Builder
	line := func(format string, args ...any) {
		sb.WriteString(fmt.Sprintf(format, args...))
		sb.WriteByte('\n')
	}
	line("# Bug 排障上下文")
	line("")
	line("## Bug")
	line("- 来源: %s:%s", emptyDash(b.Source), emptyDash(b.SourceID))
	line("- 标题: %s", emptyDash(b.Title))
	line("- 状态: %s", emptyDash(b.Status))
	line("- 严重级别: %s", emptyDash(b.Severity))
	line("- 环境: %s", emptyDash(b.Env))
	line("- 前端入口: %s", emptyDash(b.FrontendRepo))
	line("- 前端 URL: %s", emptyDash(b.FrontendURL))
	line("")
	line("## 复现")
	line("%s", emptyDash(b.Steps))
	if b.Expected != "" || b.Actual != "" {
		line("")
		line("## 预期/实际")
		line("- 预期: %s", emptyDash(b.Expected))
		line("- 实际: %s", emptyDash(b.Actual))
	}
	writeList(&sb, "API 路径", b.APIPaths)
	writeList(&sb, "Trace IDs", b.TraceIDs)
	writeList(&sb, "Request IDs", b.RequestIDs)
	if len(b.Attachments) > 0 {
		line("")
		line("## 附件")
		for _, a := range b.Attachments {
			ref := a.LocalPath
			if ref == "" {
				ref = a.RemoteURL
			}
			line("- %s %s", a.Name, emptyDash(ref))
		}
	}
	line("")
	line("## 选定机器人")
	line("- system_id: %s", emptyDash(bot.SystemID))
	line("- target: %s", emptyDash(bot.Target))
	line("- path: %s", emptyDash(bot.Path))
	line("")
	line("## 建议首轮排查")
	line("1. 优先根据 trace/request id 查链路与日志。")
	line("2. 若没有 trace id,用 API 路径和前端入口映射候选后端服务。")
	line("3. 对照最近变更、K8s 状态、配置中心和下游依赖定位根因。")
	return strings.TrimSpace(sb.String()) + "\n"
}

func writeList(sb *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	sb.WriteString("\n## " + title + "\n")
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			sb.WriteString("- " + item + "\n")
		}
	}
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
```

```go
// internal/bughub/match.go
package bughub

import (
	"sort"
	"strings"
)

func MatchBots(b Bug, bots []BotRef) []BotMatch {
	out := make([]BotMatch, 0, len(bots))
	for _, bot := range bots {
		score := 0
		var reasons []string
		if sameText(bot.SystemID, b.SystemID) && b.SystemID != "" {
			score += 50
			reasons = append(reasons, "system_id matched")
		}
		if sameText(bot.Env, b.Env) && b.Env != "" {
			score += 20
			reasons = append(reasons, "env matched")
		}
		haystack := strings.ToLower(bot.Name + " " + bot.Path + " " + bot.SystemID)
		for _, term := range append([]string{b.FrontendRepo}, b.ServiceHints...) {
			term = strings.ToLower(strings.TrimSpace(term))
			if term != "" && strings.Contains(haystack, term) {
				score += 10
				reasons = append(reasons, "hint matched: "+term)
			}
		}
		out = append(out, BotMatch{Bot: bot, Score: score, Reasons: reasons})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

func sameText(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/bughub -count=1
```

Expected: PASS.

## Task 3: Zentao Normalization And Pull Client

**Files:**
- Create: `internal/bughub/zentao.go`
- Create: `internal/bughub/zentao_test.go`

- [ ] **Step 1: Write failing Zentao tests**

```go
package bughub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeZentaoBug(t *testing.T) {
	raw := ZentaoBug{
		ID: "1842", Title: "支付页提交后 500", Status: "active",
		AssignedTo: "xiaolong", OpenedBy: "qa", Severity: "1", Pri: "2",
		Steps: "打开支付页", Keywords: "prod,mall-web,pay-service",
	}

	got := NormalizeZentaoBug(raw)

	if got.ID != "zentao-1842" || got.Source != "zentao" || got.SourceID != "1842" {
		t.Fatalf("identity mismatch: %+v", got)
	}
	if got.Assignee != "xiaolong" || got.Reporter != "qa" {
		t.Fatalf("people mismatch: %+v", got)
	}
	if got.Env != "prod" {
		t.Fatalf("env = %q", got.Env)
	}
	if len(got.ServiceHints) == 0 || got.ServiceHints[0] != "pay-service" {
		t.Fatalf("service hints = %#v", got.ServiceHints)
	}
}

func TestZentaoClientFetchAssigned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/bugs" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Token") != "secret" {
			t.Fatalf("Token header = %q", r.Header.Get("Token"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"}]}`))
	}))
	defer srv.Close()

	client := ZentaoClient{BaseURL: srv.URL, Token: "secret", HTTPClient: srv.Client()}
	got, err := client.FetchAssigned("xiaolong")
	if err != nil {
		t.Fatalf("FetchAssigned: %v", err)
	}
	if len(got) != 1 || got[0].ID != "zentao-1842" {
		t.Fatalf("bugs = %+v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify RED**

Run:

```bash
go test ./internal/bughub -run TestZentao -count=1
```

Expected: FAIL with undefined Zentao types.

- [ ] **Step 3: Implement Zentao client**

```go
package bughub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ZentaoBug struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	AssignedTo string `json:"assignedTo"`
	OpenedBy   string `json:"openedBy"`
	Severity   string `json:"severity"`
	Pri        string `json:"pri"`
	Steps      string `json:"steps"`
	Keywords   string `json:"keywords"`
}

type ZentaoClient struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

func NormalizeZentaoBug(raw ZentaoBug) Bug {
	env, frontend, hints := parseZentaoKeywords(raw.Keywords)
	return Bug{
		ID:          "zentao-" + raw.ID,
		Source:      "zentao",
		SourceID:    raw.ID,
		Title:       raw.Title,
		Status:      raw.Status,
		Severity:    raw.Severity,
		Priority:    raw.Pri,
		Assignee:    raw.AssignedTo,
		Reporter:    raw.OpenedBy,
		Steps:       raw.Steps,
		Env:         env,
		FrontendRepo: frontend,
		ServiceHints: hints,
		UpdatedAt:   time.Now().UTC(),
		RawPreview:  raw.Title,
	}
}

func (c ZentaoClient) FetchAssigned(account string) ([]Bug, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	u, err := url.Parse(base + "/api.php/v1/bugs")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if account != "" {
		q.Set("assignedTo", account)
	}
	u.RawQuery = q.Encode()
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Token", c.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("zentao returned %s", resp.Status)
	}
	var payload struct {
		Bugs []ZentaoBug `json:"bugs"`
		Data []ZentaoBug `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	raw := payload.Bugs
	if len(raw) == 0 {
		raw = payload.Data
	}
	out := make([]Bug, 0, len(raw))
	for _, item := range raw {
		out = append(out, NormalizeZentaoBug(item))
	}
	return out, nil
}

func parseZentaoKeywords(keywords string) (env string, frontend string, hints []string) {
	for _, part := range strings.Split(keywords, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "prod" || part == "test" || part == "dev" || part == "staging":
			env = part
		case strings.HasSuffix(part, "-web") || strings.HasSuffix(part, "-admin"):
			frontend = part
		case part != "":
			hints = append(hints, part)
		}
	}
	return env, frontend, hints
}
```

- [ ] **Step 4: Run Zentao tests**

Run:

```bash
go test ./internal/bughub -run TestZentao -count=1
```

Expected: PASS.

## Task 4: Desktop Wails Bindings

**Files:**
- Create: `cmd/tshoot-desktop/bindings_bug.go`
- Test: `go test ./cmd/tshoot-desktop ./internal/bughub`

- [ ] **Step 1: Write binding implementation**

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

type BugImportInput struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Steps       string   `json:"steps"`
	Env         string   `json:"env"`
	FrontendURL string   `json:"frontend_url"`
	APIPaths    []string `json:"api_paths"`
	TraceIDs    []string `json:"trace_ids"`
}

type ZentaoRefreshInput struct {
	BaseURL  string `json:"base_url"`
	Token    string `json:"token"`
	Account  string `json:"account"`
}

type BugContextInput struct {
	BugID string        `json:"bug_id"`
	Bot   bughub.BotRef `json:"bot"`
}

func (a *App) ListBugs() ([]bughub.Bug, error) {
	return bugStore().List()
}

func (a *App) ImportBug(input BugImportInput) (bughub.Bug, error) {
	now := time.Now().UTC()
	b := bughub.Bug{
		ID: "manual-" + now.Format("20060102150405.000000000"),
		Source: "manual", Title: strings.TrimSpace(input.Title),
		Description: input.Description, Steps: input.Steps, Env: input.Env,
		FrontendURL: input.FrontendURL, APIPaths: input.APIPaths, TraceIDs: input.TraceIDs,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := bugStore().Upsert(b); err != nil {
		return bughub.Bug{}, err
	}
	return b, nil
}

func (a *App) RefreshZentaoBugs(input ZentaoRefreshInput) ([]bughub.Bug, error) {
	client := bughub.ZentaoClient{BaseURL: input.BaseURL, Token: input.Token}
	items, err := client.FetchAssigned(input.Account)
	if err != nil {
		return nil, err
	}
	store := bugStore()
	for _, item := range items {
		if err := store.Upsert(item); err != nil {
			return nil, err
		}
	}
	return store.List()
}

func (a *App) MatchBugBots(bugID string) ([]bughub.BotMatch, error) {
	bugs, err := bugStore().List()
	if err != nil {
		return nil, err
	}
	var selected bughub.Bug
	for _, item := range bugs {
		if item.ID == bugID {
			selected = item
			break
		}
	}
	agents, err := discover.Scan(nil)
	if err != nil {
		return nil, err
	}
	bots := make([]bughub.BotRef, 0, len(agents))
	for _, ag := range agents {
		bots = append(bots, bughub.BotRef{
			Key: ag.Path + "|" + ag.Meta.Target,
			SystemID: ag.Meta.SystemID,
			Target: ag.Meta.Target,
			Path: ag.Path,
			Name: ag.Meta.AgentID,
		})
	}
	return bughub.MatchBots(selected, bots), nil
}

func (a *App) GenerateBugContext(input BugContextInput) (string, error) {
	bugs, err := bugStore().List()
	if err != nil {
		return "", err
	}
	for _, item := range bugs {
		if item.ID == input.BugID {
			return bughub.GenerateContext(item, input.Bot), nil
		}
	}
	return "", os.ErrNotExist
}

func bugStore() *bughub.Store {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return bughub.NewStore(filepath.Join(home, ".tshoot", "bugs"))
}
```

- [ ] **Step 2: Run binding package tests**

Run:

```bash
go test ./cmd/tshoot-desktop ./internal/bughub
```

Expected: PASS.

- [ ] **Step 3: Regenerate Wails bindings**

Run:

```bash
make wails-gen
```

Expected: `web/wailsjs/go/main/App.{d.ts,js}` include `ListBugs`, `ImportBug`, `RefreshZentaoBugs`, `MatchBugBots`, and `GenerateBugContext`.

## Task 5: Frontend Bridge And Route

**Files:**
- Create: `web/src/lib/bridge/bugs.ts`
- Create: `web/src/lib/bridge/bugs.test.ts`
- Modify: `web/src/lib/bridge.ts`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/App.vue`

- [ ] **Step 1: Write failing bridge test**

```ts
import { afterEach, describe, expect, it, vi } from 'vitest'
import { listBugs, importBug } from './bugs'

afterEach(() => {
  vi.restoreAllMocks()
  ;(window as any).go = undefined
})

describe('bug bridge', () => {
  it('returns [] in browser mode for listBugs', async () => {
    expect(await listBugs()).toEqual([])
  })

  it('forwards importBug to Wails in desktop mode', async () => {
    const spy = vi.fn().mockResolvedValue({ id: 'manual-1', title: 'Bug' })
    ;(window as any).go = { main: { App: { ImportBug: spy } } }

    const result = await importBug({ title: 'Bug' })

    expect(spy).toHaveBeenCalledWith({ title: 'Bug' })
    expect(result.id).toBe('manual-1')
  })
})
```

- [ ] **Step 2: Run test to verify RED**

Run:

```bash
cd web && npm test -- src/lib/bridge/bugs.test.ts
```

Expected: FAIL because `./bugs` does not exist.

- [ ] **Step 3: Implement bridge**

```ts
import * as App from '../../../wailsjs/go/main/App'
import { isDesktop } from './shared'

export interface BugRecord {
  id: string
  source: string
  source_id?: string
  title: string
  description?: string
  steps?: string
  status?: string
  severity?: string
  priority?: string
  assignee?: string
  reporter?: string
  env?: string
  system_id?: string
  frontend_repo?: string
  frontend_url?: string
  api_paths?: string[]
  trace_ids?: string[]
  request_ids?: string[]
  attachments?: Array<{ name: string; type?: string; local_path?: string; remote_url?: string }>
}

export interface BugImportInput {
  title: string
  description?: string
  steps?: string
  env?: string
  frontend_url?: string
  api_paths?: string[]
  trace_ids?: string[]
}

export interface BotRef {
  key: string
  system_id: string
  target: string
  path: string
  name?: string
  env?: string
}

export interface BotMatch {
  bot: BotRef
  score: number
  reasons: string[]
}

export async function listBugs(): Promise<BugRecord[]> {
  if (!isDesktop()) return []
  const r = await App.ListBugs()
  return Array.isArray(r) ? r as BugRecord[] : []
}

export async function importBug(input: BugImportInput): Promise<BugRecord> {
  if (!isDesktop()) throw new Error('Bug 工单只在桌面 app 可用')
  return App.ImportBug(input as any) as Promise<BugRecord>
}

export async function refreshZentaoBugs(input: { base_url: string; token: string; account: string }): Promise<BugRecord[]> {
  if (!isDesktop()) throw new Error('禅道拉取只在桌面 app 可用')
  return App.RefreshZentaoBugs(input as any) as Promise<BugRecord[]>
}

export async function matchBugBots(bugID: string): Promise<BotMatch[]> {
  if (!isDesktop()) return []
  return App.MatchBugBots(bugID) as Promise<BotMatch[]>
}

export async function generateBugContext(input: { bug_id: string; bot: BotRef }): Promise<string> {
  if (!isDesktop()) throw new Error('生成排障上下文只在桌面 app 可用')
  return App.GenerateBugContext(input as any)
}
```

- [ ] **Step 4: Re-export bridge and add route/nav**

Modify `web/src/lib/bridge.ts`:

```ts
export * from './bridge/bugs'
```

Modify `web/src/router/index.ts`:

```ts
{
  path: '/bugs',
  name: 'Bugs',
  component: () => import('../pages/BugWorkbenchPage.vue'),
},
```

Modify `web/src/App.vue` nav items:

```ts
{ path: '/bugs', icon: '🐞', label: 'Bug 工单', desc: '接入禅道,选择机器人排障' },
```

- [ ] **Step 5: Run frontend bridge tests**

Run:

```bash
cd web && npm test -- src/lib/bridge/bugs.test.ts
```

Expected: PASS.

## Task 6: Bug Workbench Page

**Files:**
- Create: `web/src/pages/BugWorkbenchPage.vue`

- [ ] **Step 1: Implement page**

```vue
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  type BotMatch,
  type BugRecord,
  generateBugContext,
  importBug,
  listBugs,
  matchBugBots,
  refreshZentaoBugs,
} from '../lib/bridge'
import { toast, toastError } from '../lib/toast'
import { copyText } from '../lib/clipboard'

const bugs = ref<BugRecord[]>([])
const selectedID = ref('')
const matches = ref<BotMatch[]>([])
const selectedBotKey = ref('')
const contextText = ref('')
const loading = ref(false)
const query = ref('')
const importOpen = ref(false)
const manual = ref({ title: '', env: '', frontend_url: '', steps: '', description: '' })
const zentao = ref({ base_url: '', token: '', account: '' })

const selectedBug = computed(() => bugs.value.find(b => b.id === selectedID.value) || bugs.value[0])
const filteredBugs = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return bugs.value
  return bugs.value.filter(b =>
    [b.title, b.source_id, b.env, b.frontend_repo].some(v => (v || '').toLowerCase().includes(q)),
  )
})
const selectedBot = computed(() => matches.value.find(m => m.bot.key === selectedBotKey.value)?.bot)

async function load() {
  loading.value = true
  try {
    bugs.value = await listBugs()
    if (!selectedID.value && bugs.value[0]) selectedID.value = bugs.value[0].id
    await loadMatches()
  } catch (e) {
    toastError('加载 Bug 工单', e)
  } finally {
    loading.value = false
  }
}

async function selectBug(id: string) {
  selectedID.value = id
  contextText.value = ''
  await loadMatches()
}

async function loadMatches() {
  if (!selectedBug.value) return
  matches.value = await matchBugBots(selectedBug.value.id)
  selectedBotKey.value = matches.value[0]?.bot.key || ''
}

async function submitManualBug() {
  if (!manual.value.title.trim()) {
    toast.error('Bug 标题必填')
    return
  }
  const bug = await importBug({
    title: manual.value.title,
    env: manual.value.env,
    frontend_url: manual.value.frontend_url,
    steps: manual.value.steps,
    description: manual.value.description,
  })
  importOpen.value = false
  await load()
  selectedID.value = bug.id
}

async function pullZentao() {
  try {
    bugs.value = await refreshZentaoBugs(zentao.value)
    if (!selectedID.value && bugs.value[0]) selectedID.value = bugs.value[0].id
    await loadMatches()
    toast.success(`已刷新 ${bugs.value.length} 条 Bug`)
  } catch (e) {
    toastError('禅道刷新', e)
  }
}

async function buildContext() {
  if (!selectedBug.value || !selectedBot.value) {
    toast.error('请先选择 Bug 和机器人')
    return
  }
  contextText.value = await generateBugContext({ bug_id: selectedBug.value.id, bot: selectedBot.value })
}

async function copyContext() {
  if (!contextText.value) return
  if (await copyText(contextText.value)) toast.success('排障上下文已复制')
}

onMounted(load)
</script>

<template>
  <div class="bug-page">
    <header class="page-header">
      <div>
        <h1>Bug 工单</h1>
        <p class="subtitle">接入禅道或手动导入 Bug,选择已安装排障机器人生成排查上下文。</p>
      </div>
      <button class="btn primary" @click="importOpen = !importOpen">手动导入</button>
    </header>

    <section class="zentao-bar">
      <input v-model="zentao.base_url" placeholder="禅道地址 https://zentao.example.com" />
      <input v-model="zentao.account" placeholder="指派账号" />
      <input v-model="zentao.token" placeholder="Token" type="password" />
      <button class="btn" @click="pullZentao">拉取指派给我的 Bug</button>
    </section>

    <section v-if="importOpen" class="manual-box">
      <input v-model="manual.title" placeholder="Bug 标题" />
      <input v-model="manual.env" placeholder="环境 prod/test/dev" />
      <input v-model="manual.frontend_url" placeholder="前端 URL" />
      <textarea v-model="manual.steps" placeholder="复现步骤"></textarea>
      <textarea v-model="manual.description" placeholder="补充描述"></textarea>
      <button class="btn primary" @click="submitManualBug">保存 Bug</button>
    </section>

    <div class="bug-layout">
      <aside class="bug-list">
        <input v-model="query" class="search" placeholder="搜索 Bug" />
        <button
          v-for="bug in filteredBugs"
          :key="bug.id"
          class="bug-item"
          :class="{ active: selectedBug?.id === bug.id }"
          @click="selectBug(bug.id)"
        >
          <strong>{{ bug.source_id ? `#${bug.source_id} ` : '' }}{{ bug.title }}</strong>
          <span>{{ bug.env || '未标环境' }} · {{ bug.status || bug.source }}</span>
        </button>
        <div v-if="!loading && filteredBugs.length === 0" class="empty">暂无 Bug,可手动导入或拉取禅道。</div>
      </aside>

      <main class="bug-detail">
        <template v-if="selectedBug">
          <div class="detail-head">
            <div>
              <div class="muted">{{ selectedBug.source }} {{ selectedBug.source_id }}</div>
              <h2>{{ selectedBug.title }}</h2>
            </div>
            <span class="severity">{{ selectedBug.severity || selectedBug.priority || '未定级' }}</span>
          </div>
          <div class="meta-grid">
            <div><span>环境</span><strong>{{ selectedBug.env || '-' }}</strong></div>
            <div><span>指派</span><strong>{{ selectedBug.assignee || '-' }}</strong></div>
            <div><span>前端入口</span><strong>{{ selectedBug.frontend_repo || '-' }}</strong></div>
            <div><span>状态</span><strong>{{ selectedBug.status || '-' }}</strong></div>
          </div>
          <section>
            <h3>复现步骤</h3>
            <pre>{{ selectedBug.steps || selectedBug.description || '暂无复现描述' }}</pre>
          </section>
          <section>
            <h3>线索</h3>
            <p>URL: {{ selectedBug.frontend_url || '-' }}</p>
            <p>API: {{ selectedBug.api_paths?.join(', ') || '-' }}</p>
            <p>Trace: {{ selectedBug.trace_ids?.join(', ') || '-' }}</p>
          </section>
        </template>
      </main>

      <aside class="bot-panel">
        <h3>选择排障机器人</h3>
        <button
          v-for="m in matches"
          :key="m.bot.key"
          class="bot-match"
          :class="{ active: selectedBotKey === m.bot.key }"
          @click="selectedBotKey = m.bot.key"
        >
          <strong>{{ m.bot.system_id || m.bot.name || m.bot.key }}</strong>
          <span>{{ m.bot.target }} · score {{ m.score }}</span>
        </button>
        <div v-if="matches.length === 0" class="empty">未发现已装机器人。</div>
        <button class="btn primary full" @click="buildContext">生成排障上下文</button>
        <button class="btn full" :disabled="!contextText" @click="copyContext">复制上下文</button>
        <textarea v-model="contextText" class="context-preview" readonly placeholder="生成后在这里预览"></textarea>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.bug-page { max-width: 1500px; margin: 0 auto; }
.page-header { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; margin-bottom:16px; }
.subtitle,.muted,.empty { color:#64748b; font-size:13px; }
.btn { border:1px solid #cbd5e1; background:#fff; color:#334155; border-radius:6px; padding:8px 12px; cursor:pointer; }
.btn.primary { background:#2563eb; color:white; border-color:#2563eb; }
.btn.full { width:100%; margin-top:8px; }
.zentao-bar,.manual-box { display:flex; gap:8px; padding:12px; border:1px solid #e2e8f0; border-radius:8px; margin-bottom:14px; background:#f8fafc; }
.manual-box { flex-direction:column; }
input,textarea { border:1px solid #cbd5e1; border-radius:6px; padding:8px 10px; font:inherit; }
textarea { min-height:80px; resize:vertical; }
.bug-layout { display:grid; grid-template-columns:300px minmax(0,1fr) 320px; min-height:620px; border:1px solid #e2e8f0; border-radius:8px; overflow:hidden; }
.bug-list,.bot-panel { background:#f8fafc; padding:12px; overflow:auto; }
.bug-list { border-right:1px solid #e2e8f0; }
.bot-panel { border-left:1px solid #e2e8f0; }
.search { width:100%; margin-bottom:10px; }
.bug-item,.bot-match { display:block; width:100%; text-align:left; border:1px solid #e2e8f0; background:white; border-radius:7px; padding:10px; margin-bottom:8px; cursor:pointer; }
.bug-item.active,.bot-match.active { border-color:#2563eb; background:#eff6ff; }
.bug-item span,.bot-match span { display:block; margin-top:5px; color:#64748b; font-size:12px; }
.bug-detail { padding:20px; overflow:auto; }
.detail-head { display:flex; justify-content:space-between; gap:16px; align-items:flex-start; }
.severity { background:#fee2e2; color:#991b1b; border-radius:6px; padding:5px 8px; font-size:12px; font-weight:700; }
.meta-grid { display:grid; grid-template-columns:repeat(4,1fr); gap:8px; margin:16px 0; }
.meta-grid div { border:1px solid #e2e8f0; background:#f8fafc; border-radius:6px; padding:9px; }
.meta-grid span { display:block; color:#64748b; font-size:11px; }
section { margin-top:18px; }
pre { white-space:pre-wrap; background:#f8fafc; border:1px solid #e2e8f0; border-radius:7px; padding:12px; color:#334155; }
.context-preview { width:100%; min-height:240px; margin-top:10px; font-family:ui-monospace,Menlo,monospace; font-size:12px; }
</style>
```

- [ ] **Step 2: Run frontend build/test**

Run:

```bash
cd web && npm test -- src/lib/bridge/bugs.test.ts
cd web && npm run build
```

Expected: tests and build pass.

## Task 7: Final Verification

**Files:**
- All files above.

- [ ] **Step 1: Run targeted verification**

```bash
go test ./internal/bughub ./cmd/tshoot-desktop
cd web && npm test -- src/lib/bridge/bugs.test.ts
cd web && npm run build
```

Expected: all pass.

- [ ] **Step 2: Run repo verification**

```bash
go test ./... -race
git diff --check
```

Expected: all pass; no whitespace errors.

- [ ] **Step 3: Manual smoke**

```bash
make desktop
```

Expected: desktop binary builds. In the app, `/bugs` opens from sidebar, manual import creates a bug, selecting a bot generates context.

## Self-Review

- Spec coverage: covered sidebar/page, manual import, Zentao API pull, installed bot selection, context generation, and tests. Webhook and IDE auto-control are explicitly out of scope.
- Placeholder scan: no TBD/TODO placeholders. Later work is explicitly scoped outside first version.
- Type consistency: Go JSON field names use snake_case where frontend sends raw objects; frontend interfaces match binding input names.
