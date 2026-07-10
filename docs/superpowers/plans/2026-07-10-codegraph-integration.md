# CodeGraph Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 Studio 生成的排障机器人增加可显式启用、固定版本安装、逐仓索引、通过单个 MCP 查询并可稳定降级到 `rg/read` 的 CodeGraph 代码智能能力。

**Architecture:** 保留 `internal/analyzer` 作为部署期确定性扫描主路径，新增独立的 CodeGraph 安装器、索引管理器和运行时查询 skill。部署层按配置确保 binary、并发预热所有可分析仓库、注册一个共享 MCP；self-test 分别验证 MCP 工具面和逐仓索引健康，任一增强链路失败都只产生可见 WARN，不阻断机器人部署。

**Tech Stack:** Go 1.x、YAML v3、Wails v2、Vue 3、TypeScript、Vitest、CodeGraph `v1.3.1` release bundle、MCP stdio JSON-RPC、Tree-sitter/SQLite（由 CodeGraph bundle 内置）。

## Global Constraints

- `code_intelligence.enabled` 默认 `false`；首版唯一合法 provider 是 `codegraph`。
- 固定 CodeGraph `v1.3.1`，不得查询 `latest`，不得执行 `codegraph install`，不得使用 `curl | sh`。
- 下载必须先校验本计划列出的固定 SHA-256，再解包或执行。
- Studio 管理的 CodeGraph 进程必须设置 `CODEGRAPH_TELEMETRY=0`、`DO_NOT_TRACK=1`；不设置 `CODEGRAPH_MCP_TOOLS`，只使用上游默认 `codegraph_explore`。
- 不向 MCP builder 添加默认硬禁写参数；遵守项目 MCP 软约束。
- 所有 `repos[].analysis.enabled: true` 且有有效本地路径的源码仓库都参加预索引；仓库并发上限固定为 2。
- `init` 超时 120 秒，`sync` 超时 30 秒；单仓失败不得中断其他仓库或阻断部署。
- CodeGraph 不替换 `internal/analyzer`，不修改 `analysis.json` 契约，不自动 checkout、切分支或创建 worktree。
- 只注册一个 `cfg.MCPKeyPrefix()+"-codegraph"` MCP（例如 `shop-codegraph`）；跨仓查询必须显式传 routing 提供的绝对 `projectPath`。
- 卸载机器人不删除 `~/.tshoot` 下共享 bundle，也不删除业务仓库的 `.codegraph/`。
- 普通单测不得联网或下载真实 bundle；真实 binary/MCP 验证只放在显式 smoke test。
- 修改 MCP builder 必须同步 positive test、negative test、`requiredMCPKeys` 和真实 runtime probe。
- 修改 schema 必须同步 Go config、全部顶层 `examples/*.yaml`、向导生成/导入和测试。
- 当前工作树已有用户改动；每次提交只 `git add` 本任务列出的文件，禁止 `git add -A`。

---

## File/Interface Map

| Responsibility | Files | Public contract |
|---|---|---|
| YAML config | `internal/config/types_code_intelligence.go`, `internal/config/types.go`, `internal/config/validate.go` | `CodeIntelligence`, `UsesCodeGraph()` |
| Generator gating | `internal/generator/render_walk.go`, `internal/generator/funcs.go`, `internal/generator/plan.go` | disabled means skill is absent even with empty whitelist |
| Runtime skill | `templates/workspace/skills/code-intelligence-query/SKILL.md.tmpl`, `templates/workspace/skills/incident-investigator/SKILL.md.tmpl` | branch/freshness/query/fallback protocol |
| Bundle install | `internal/agent/ensure_codegraph.go` | `EnsureCodeGraphInstalled(onLog) (absoluteCommand, error)` |
| Index lifecycle | `internal/agent/codegraph_index.go` | `PrepareCodeGraphIndexes(ctx, options) CodeGraphIndexReport` |
| MCP registration | `internal/agent/install_native_mcp_code_intelligence.go` | `MCPBuildOptions.CodeGraphBinaryPath`, one fixed key |
| Deploy orchestration | `internal/agent/apply.go`, desktop bindings | optional report, same-process cache, retry binding |
| Self-test | `internal/agent/self_test_codegraph.go` | MCP tool assertion and repository status checks remain separate |
| Wizard | `web/src/lib/yamlGenerator.ts`, `web/src/lib/yamlImporter.ts`, `web/src/pages/InitPage.vue` | opt-in state survives draft and YAML round trip |
| UI feedback | `web/src/components/OneClickDeployStep.vue`, `web/src/lib/useDeployFlow.ts` | `CodeGraph X/Y repos ready`, per-repo details, retry |

---

### Task 1: Add the configuration contract and backward-compatible defaults

**Files:**
- Create: `internal/config/types_code_intelligence.go`
- Create: `internal/config/code_intelligence_test.go`
- Modify: `internal/config/types.go` (`SystemConfig`)
- Modify: `internal/config/validate.go` (top-level validation before repository validation)
- Modify: `internal/config/health.go` (`knownSkills`)
- Modify: `internal/config/health_generation.go` (`code-intelligence-query` whitelist health check)
- Modify: `api/handler_test.go` (`/api/validate` round trip)
- Modify: `schema/troubleshooter.schema.yaml` (between `repos` and `infrastructure`)
- Modify: `examples/apollo-troubleshooter.yaml`
- Modify: `examples/b2b-api-troubleshooter.yaml`
- Modify: `examples/consul-troubleshooter.yaml`
- Modify: `examples/env-vars-troubleshooter.yaml`
- Modify: `examples/event-driven-troubleshooter.yaml`
- Modify: `examples/k8s-troubleshooter.yaml`
- Modify: `examples/monorepo-troubleshooter.yaml`
- Modify: `examples/shop-claude-code.yaml`
- Modify: `examples/shop-troubleshooter.yaml`
- Modify: `examples/three-tier-troubleshooter.yaml`

**Interfaces:**
- Consumes: existing `config.LoadFromBytes`, `config.Validate`, `config.HealthCheck`.
- Produces:

```go
const CodeIntelligenceProviderCodeGraph = "codegraph"

type CodeIntelligence struct {
    Enabled  bool   `yaml:"enabled"`
    Provider string `yaml:"provider,omitempty"`
}

func (c CodeIntelligence) UsesCodeGraph() bool
```

- [ ] **Step 1: Write failing config tests**

Create `internal/config/code_intelligence_test.go`. Reuse the same-package `minimalValid()` helper from `loader_test.go`, and add this local round-trip helper:

```go
func loadCodeIntelligenceConfig(t *testing.T, ci CodeIntelligence) *SystemConfig {
    t.Helper()
    c := minimalValid()
    c.CodeIntelligence = ci
    data, err := yaml.Marshal(&c)
    if err != nil {
        t.Fatal(err)
    }
    cfg, err := LoadFromBytes(data)
    if err != nil {
        t.Fatal(err)
    }
    return cfg
}
```

Then assert:

```go
func TestCodeIntelligence_DefaultDisabled(t *testing.T) {
    cfg := loadCodeIntelligenceConfig(t, CodeIntelligence{})
    if cfg.CodeIntelligence.Enabled || cfg.CodeIntelligence.UsesCodeGraph() {
        t.Fatalf("zero value must be disabled: %#v", cfg.CodeIntelligence)
    }
}

func TestCodeIntelligence_CodeGraphEnabled(t *testing.T) {
    cfg := loadCodeIntelligenceConfig(t, CodeIntelligence{Enabled: true, Provider: "codegraph"})
    if !cfg.CodeIntelligence.UsesCodeGraph() {
        t.Fatalf("expected codegraph enabled: %#v", cfg.CodeIntelligence)
    }
}

func TestCodeIntelligence_RejectsMissingOrUnknownProvider(t *testing.T) {
    for _, provider := range []string{"", "lsp", "sourcegraph"} {
        c := minimalValid()
        c.CodeIntelligence = CodeIntelligence{Enabled: true, Provider: provider}
        err := Validate(&c)
        if err == nil || !strings.Contains(err.Error(), "code_intelligence.provider") {
            t.Fatalf("provider=%q err=%v", provider, err)
        }
    }
}

func TestHealthCheck_CodeIntelligenceSkillWasTrimmed(t *testing.T) {
    cfg := loadCodeIntelligenceConfig(t, CodeIntelligence{Enabled: true, Provider: "codegraph"})
    cfg.Generation.SkillsWhitelist = []string{"routing", "incident-investigator"}
    issues := HealthCheck(cfg)
    for _, issue := range issues {
        if issue.Category == "generation" && strings.Contains(issue.Message, "code-intelligence-query") {
            return
        }
    }
    t.Fatalf("missing trimmed-skill issue: %#v", issues)
}
```

The local helpers must construct a fully valid one-environment/one-repository config and scan returned issues by category/message; do not reuse unexported helpers from unrelated tests.

- [ ] **Step 2: Run the focused tests and confirm the contract does not exist yet**

Run: `go test ./internal/config/ -run 'TestCodeIntelligence|TestHealthCheck_CodeIntelligence'`

Expected: FAIL because `SystemConfig.CodeIntelligence`, `UsesCodeGraph`, and validation are undefined.

- [ ] **Step 3: Implement the Go type, field, validation, and health behavior**

Create `types_code_intelligence.go` with the exact interface above. Add this field to `SystemConfig`:

```go
CodeIntelligence CodeIntelligence `yaml:"code_intelligence,omitempty"`
```

Add validation with these exact rules:

```go
if c.CodeIntelligence.Enabled && c.CodeIntelligence.Provider == "" {
    return fmt.Errorf("code_intelligence.provider required when enabled")
}
if p := c.CodeIntelligence.Provider; p != "" && p != CodeIntelligenceProviderCodeGraph {
    return fmt.Errorf("code_intelligence.provider=%q invalid (valid: codegraph)", p)
}
```

Add `"code-intelligence-query": true` to `knownSkills`. When CodeGraph is enabled, whitelist is non-empty, and the skill is absent, append this health issue:

```go
HealthIssue{
    Severity: "info",
    Category: "generation",
    Field:    "generation.skills_whitelist",
    Message:  "code_intelligence 已启用,但 skills_whitelist 未包含 code-intelligence-query,代码图谱能力会被裁剪",
    Hint:     "加入 code-intelligence-query,或关闭 code_intelligence.enabled",
}
```

- [ ] **Step 4: Update schema and every example without changing default behavior**

Add this documented block to the schema example:

```yaml
code_intelligence:
  enabled: false                    # 显式 opt-in；关闭时不下载、不索引、不注册 MCP
  provider: codegraph               # 首版只支持 codegraph
```

Add the same disabled block to each listed example immediately before `infrastructure:`. Do not enable it in default fixtures; the opt-in example belongs in README in Task 10.

- [ ] **Step 5: Run config and example parsing tests**

Add `TestHandleValidate_CodeIntelligence` to POST `minimalYAML` with the enabled block inserted before `infrastructure:` and assert HTTP 200/`valid:true`; post the same YAML with `provider: lsp` and assert HTTP 400 with an error containing `code_intelligence.provider`.

Run: `go test ./internal/config/ ./internal/generator/ ./api/ -run 'TestCodeIntelligence|TestHealthCheck_CodeIntelligence|TestGenerate_Nacos_Shop|TestGenerate_MultiTargets|TestHandleValidate_CodeIntelligence'`

Expected: PASS; existing examples continue to generate the same runtime capabilities because CodeGraph remains disabled.

- [ ] **Step 6: Commit the configuration contract**

```bash
git add internal/config/types_code_intelligence.go internal/config/code_intelligence_test.go internal/config/types.go internal/config/validate.go internal/config/health.go internal/config/health_generation.go api/handler_test.go schema/troubleshooter.schema.yaml examples/*.yaml
git commit -m "feat: add opt-in code intelligence config"
```

---

### Task 2: Generate the CodeGraph skill only when both config and whitelist allow it

**Files:**
- Create: `templates/workspace/skills/code-intelligence-query/SKILL.md.tmpl`
- Modify: `templates/workspace/skills/incident-investigator/SKILL.md.tmpl` (Step 5.2)
- Modify: `internal/generator/render_walk.go` (`shouldSkipDir`)
- Modify: `internal/generator/funcs.go` (`hasSkill`)
- Modify: `internal/generator/plan.go` (`skipReason`)
- Modify: `internal/generator/readme.go` (`skillDesc` and FAQ)
- Modify: `internal/generator/generator_test.go`
- Modify: `internal/generator/chain_integrity_test.go`

**Interfaces:**
- Consumes: `config.CodeIntelligence.UsesCodeGraph()`, routing references already generated under `skills/routing/references/`.
- Produces: internal helper `skillEnabled(ctx *Context, name string) bool`; generated `skills/code-intelligence-query/SKILL.md`.

- [ ] **Step 1: Add three failing generator tests**

Add tests using the existing `loadCfg`, `projectRoot`, `New`, `readFile`, `assertExists`, and `assertNotExists` helpers:

```go
func TestGenerate_CodeIntelligenceOptIn(t *testing.T) {
    cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
    cfg.CodeIntelligence = config.CodeIntelligence{Enabled: true, Provider: "codegraph"}
    cfg.Generation.SkillsWhitelist = []string{"routing", "incident-investigator", "code-intelligence-query"}
    out := t.TempDir()
    g := New(cfg, filepath.Join(projectRoot(t), "templates"), out)
    if err := g.Generate(); err != nil { t.Fatal(err) }
    assertExists(t, out, []string{"templates/workspace-template/skills/code-intelligence-query/SKILL.md"})
    if got := readFile(t, filepath.Join(out, "templates/workspace-template/skills/code-intelligence-query/SKILL.md")); !strings.Contains(got, "codegraph_explore") { t.Fatal(got) }
    if got := readFile(t, filepath.Join(out, "templates/workspace-template/skills/incident-investigator/SKILL.md")); !strings.Contains(got, "code-intelligence-query") { t.Fatal(got) }
}

func TestGenerate_CodeIntelligenceDisabledEvenWithOpenWhitelist(t *testing.T) {
    cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
    cfg.Generation.SkillsWhitelist = nil
    out := t.TempDir()
    g := New(cfg, filepath.Join(projectRoot(t), "templates"), out)
    if err := g.Generate(); err != nil { t.Fatal(err) }
    assertNotExists(t, out, []string{"templates/workspace-template/skills/code-intelligence-query"})
    if got := readFile(t, filepath.Join(out, "templates/workspace-template/skills/incident-investigator/SKILL.md")); strings.Contains(got, "调用 code-intelligence-query") { t.Fatal(got) }
}

func TestGenerate_CodeIntelligenceWhitelistCanTrimSkill(t *testing.T) {
    cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
    cfg.CodeIntelligence = config.CodeIntelligence{Enabled: true, Provider: "codegraph"}
    cfg.Generation.SkillsWhitelist = []string{"routing", "incident-investigator"}
    out := t.TempDir()
    g := New(cfg, filepath.Join(projectRoot(t), "templates"), out)
    if err := g.Generate(); err != nil { t.Fatal(err) }
    assertNotExists(t, out, []string{"templates/workspace-template/skills/code-intelligence-query"})
}
```

Extend chain integrity to require every literal `skills/...` path emitted by the new skill to exist in the generated workspace.

- [ ] **Step 2: Run generator tests and confirm disabled generation currently leaks the new directory**

Run: `go test ./internal/generator/ -run 'TestGenerate_CodeIntelligence|TestSkillScriptPathsExist'`

Expected: FAIL because the skill template and config-aware gating do not exist.

- [ ] **Step 3: Centralize skill enablement and use it from rendering and templates**

Implement:

```go
func skillEnabled(ctx *Context, name string) bool {
    if name == "code-intelligence-query" && !ctx.CodeIntelligence.UsesCodeGraph() {
        return false
    }
    whitelist := ctx.Generation.SkillsWhitelist
    return len(whitelist) == 0 || slices.Contains(whitelist, name)
}
```

Use it in `shouldSkipDir` before datastore/config-center checks and in `funcMap()["hasSkill"]`. Add `skipReason` result `code_intelligence.enabled=false` for this skill, while retaining `not in skills_whitelist` when CodeGraph is enabled but explicitly trimmed.

- [ ] **Step 4: Write the complete runtime query skill**

The new SKILL frontmatter description must say it is for function/call/impact evidence after routing identifies a repository. Its body must encode this exact protocol:

```text
1. Read repo-path-map.yaml, env-branch-map.yaml, repo-stack-map.yaml.
2. Set CodeGraph projectPath to repo-path-map.local_path. For monorepos, derive a separate servicePath as local_path/sub_path; use servicePath to narrow the question and rg/read fallback, but keep projectPath at the indexed repository root.
3. Run git -C "$projectPath" branch --show-current and git -C "$projectPath" rev-parse HEAD.
4. Compare with `environments.${env}.repos.${repo}`; never checkout or modify the repository.
5. Read $HOME/.tshoot/bin/codegraph (Windows: codegraph.cmd). Run status "$projectPath" --json.
6. If initialized and state=complete and branch matches, run timeout-limited sync "$projectPath"; then call codegraph_explore with projectPath and maxFiles=4.
7. Query with an error string, endpoint, business field, stack symbol, or file name; do not ask broad architecture questions.
8. If branch mismatches, label evidence low confidence and use it only as a candidate.
9. If sync fails, query the existing complete index as stale and verify every critical file/line with rg plus file read.
10. If status is absent/incomplete, MCP fails once, or projectPath is missing, stop retrying CodeGraph for the session and use git diff + rg + Read.
11. Treat returned source lines as code facts; treat calls/dynamic dispatch/blast radius as heuristic static-analysis conclusions.
12. Output repository, branch, HEAD, freshness, file:line, snippet, graph inference, and corroborating runtime evidence.
```

Use `python3 -c` with `subprocess.run(..., timeout=30)` only as the portable timeout wrapper; do not depend on GNU `timeout`. The skill must not reference a non-existent helper script.

- [ ] **Step 5: Guard incident-investigator Step 5.2 with the generated-skill condition**

Wrap the enhanced branch with:

```gotemplate
{{if hasSkill . "code-intelligence-query"}}
先 Read `skills/code-intelligence-query/SKILL.md` 并按其分支/freshness规则取证；满足条件时先调用 CodeGraph，失败立即回退到下方 git diff + rg + Read。
{{end}}
```

Keep the existing git/grep commands unconditionally present so fallback survives every configuration.

- [ ] **Step 6: Add generated README capability and fallback wording**

Map `code-intelligence-query` to `CodeGraph 函数级源码、调用关系与影响面取证；失败自动回退 rg/read`. When CodeGraph is enabled, add FAQ text explaining `.codegraph/`, target-branch mismatch, and that CodeGraph evidence cannot independently establish root cause.

- [ ] **Step 7: Run generator and script-path suites**

Run: `go test ./internal/generator/ -run 'TestGenerate_CodeIntelligence|TestSkillScriptPathsExist|TestGenerate_Nacos_Shop'`

Expected: PASS for enabled, disabled, and whitelist-trimmed cases.

- [ ] **Step 8: Commit the generated runtime capability**

```bash
git add templates/workspace/skills/code-intelligence-query/SKILL.md.tmpl templates/workspace/skills/incident-investigator/SKILL.md.tmpl internal/generator/render_walk.go internal/generator/funcs.go internal/generator/plan.go internal/generator/readme.go internal/generator/generator_test.go internal/generator/chain_integrity_test.go
git commit -m "feat: add CodeGraph runtime evidence skill"
```

---

### Task 3: Install and verify the pinned CodeGraph bundle

**Files:**
- Create: `internal/agent/ensure_codegraph.go`
- Create: `internal/agent/ensure_codegraph_test.go`

**Interfaces:**
- Consumes: `config.SystemConfig.CodeIntelligence.UsesCodeGraph()`.
- Produces:

```go
const codeGraphVersion = "v1.3.1"

func CfgUsesCodeGraph(cfg *config.SystemConfig) bool
func EnsureCodeGraphInstalled(onLog func(string)) (string, error)
func codeGraphManagedCommandPath() (string, error)
```

The returned command is `~/.tshoot/bin/codegraph` on Unix and `~/.tshoot/bin/codegraph.cmd` on Windows; it must be absolute.

- [ ] **Step 1: Write installer mapping, cache, and integrity tests**

Use injectable package variables for home, platform, and HTTP transport:

```go
var codeGraphUserHomeDir = os.UserHomeDir
var codeGraphGOOS = runtime.GOOS
var codeGraphGOARCH = runtime.GOARCH
var codeGraphHTTPClient = &http.Client{Timeout: 90 * time.Second}
```

Tests must cover these named cases: `TestCodeGraphArtifactForPlatform_AllSupported` asserts all six GOOS/GOARCH mappings and exact hashes; `TestEnsureCodeGraphInstalled_CacheHitDoesNotDownload` precreates the marker/launcher and asserts zero HTTP calls; `TestEnsureCodeGraphInstalled_RejectsSHA256Mismatch` serves a valid archive under the wrong digest and asserts no promotion; `TestEnsureCodeGraphInstalled_RejectsArchiveTraversal` serves `../../escaped`; `TestEnsureCodeGraphInstalled_DownloadFailureLeavesNoExecutable` returns HTTP 503; `TestEnsureCodeGraphInstalled_UnsupportedPlatform` sets `freebsd/amd64` and expects an actionable error.

The fake tar/zip must contain one of the real layouts, for example `codegraph-linux-x64/bin/codegraph` or `codegraph-win32-x64/bin/codegraph.cmd`, plus a harmless sibling file, so extraction behavior is exercised without network.

- [ ] **Step 2: Run installer tests and verify the installer is absent**

Run: `go test ./internal/agent/ -run 'TestCodeGraphArtifact|TestEnsureCodeGraphInstalled'`

Expected: FAIL with undefined installer symbols.

- [ ] **Step 3: Add the fixed artifact table with verified digests**

Use exactly this table:

```go
var codeGraphArtifacts = map[string]codeGraphArtifact{
    "darwin/arm64": {Asset: "codegraph-darwin-arm64.tar.gz", SHA256: "d4931334e2497a4861b214ec077d78e5e38702a258fe4e05c33ed3bc1d144a90", Format: "tar.gz", Target: "darwin-arm64"},
    "darwin/amd64": {Asset: "codegraph-darwin-x64.tar.gz", SHA256: "e9364cf8b104cf290c7c96ef1ed3dcd30d17af56583cdf0091efa0b001e3669e", Format: "tar.gz", Target: "darwin-x64"},
    "linux/arm64":  {Asset: "codegraph-linux-arm64.tar.gz", SHA256: "28130da6f6c7087d293337737dfca1040f0694996b0252c9528a7706a5721d8b", Format: "tar.gz", Target: "linux-arm64"},
    "linux/amd64":  {Asset: "codegraph-linux-x64.tar.gz", SHA256: "e605073f6eb170fe161e986c2350b6a0681e68018ed844ce57f72814c09fea1d", Format: "tar.gz", Target: "linux-x64"},
    "windows/arm64": {Asset: "codegraph-win32-arm64.zip", SHA256: "45f13d13dc7fd3dacc4c083fadec5ffa86f3e645dea7e4ca54fa057d135becef", Format: "zip", Target: "win32-arm64"},
    "windows/amd64": {Asset: "codegraph-win32-x64.zip", SHA256: "ffe76e64670f51c3335da8691174278446bd4b4af853e08c545564f4781629dd", Format: "zip", Target: "win32-x64"},
}
```

Construct the download URL as `"https://github.com/colbymchenry/codegraph/releases/download/" + codeGraphVersion + "/" + artifact.Asset`; maximum compressed body is `512 << 20` bytes.

- [ ] **Step 4: Implement safe download, hash, extraction, and atomic promotion**

Implement this sequence:

```text
cache root: filepath.Join(home, ".tshoot", "tools", "codegraph", codeGraphVersion, artifact.Target)
marker:     filepath.Join(cacheRoot, ".installed-sha256")
Unix launcher: filepath.Join(cacheRoot, "bin", "codegraph")
Windows launcher: filepath.Join(cacheRoot, "bin", "codegraph.cmd")
Unix stable command: filepath.Join(home, ".tshoot", "bin", "codegraph")
Windows stable command: filepath.Join(home, ".tshoot", "bin", "codegraph.cmd")
```

Requirements for the code step:

- stream to a temp archive under the target parent through `io.LimitReader` while calculating SHA-256;
- reject non-200 response and digest mismatch before opening the archive;
- for every tar/zip entry, clean the path and verify it stays under the temp extraction directory;
- extract regular files/directories only, preserve Unix executable bits, and reject links from the archive;
- rename the complete temp directory atomically into the versioned target directory;
- write the digest marker only after successful extraction;
- create/update the Unix stable path as a relative symlink to the real bundle launcher; the upstream launcher resolves symlinks;
- create/update the Windows stable `.cmd` with `fmt.Sprintf("@call %q %%*\r\n", bundleLauncher)`;
- on cache hit require matching marker plus an executable/regular launcher, then refresh only the stable path;
- clean temp files on every error; never delete an already valid versioned cache.

- [ ] **Step 5: Run installer tests including race detection**

Run: `go test ./internal/agent/ -race -run 'TestCodeGraphArtifact|TestEnsureCodeGraphInstalled'`

Expected: PASS; the cache-hit fake transport records zero requests and hash failure creates neither marker nor stable command.

- [ ] **Step 6: Commit the pinned installer**

```bash
git add internal/agent/ensure_codegraph.go internal/agent/ensure_codegraph_test.go
git commit -m "feat: install pinned CodeGraph bundle safely"
```

---

### Task 4: Build the bounded, cached multi-repository index manager

**Files:**
- Create: `internal/agent/codegraph_index.go`
- Create: `internal/agent/codegraph_index_test.go`

**Interfaces:**
- Consumes: absolute command returned by `EnsureCodeGraphInstalled`, `config.Repo`, deployment `RepoLocalPaths`.
- Produces:

```go
type CodeGraphRepoTarget struct {
    Name string `json:"name"`
    Path string `json:"path"`
    Head string `json:"head,omitempty"`
}

type CodeGraphIndexOptions struct {
    BinaryPath string
    SystemID string
    Repos []CodeGraphRepoTarget
    OnProgress func(string)
    InitTimeout time.Duration
    SyncTimeout time.Duration
    MaxConcurrency int
}

type CodeGraphRepoResult struct {
    Name string `json:"name"`
    Path string `json:"path"`
    Action string `json:"action"`
    Status string `json:"status"`
    Detail string `json:"detail,omitempty"`
    FileCount int `json:"file_count,omitempty"`
    NodeCount int `json:"node_count,omitempty"`
    EdgeCount int `json:"edge_count,omitempty"`
    IndexState string `json:"index_state,omitempty"`
    DurationMS int64 `json:"duration_ms"`
}

type CodeGraphIndexReport struct {
    Ready int `json:"ready"`
    Total int `json:"total"`
    Repos []CodeGraphRepoResult `json:"repos"`
}

func BuildCodeGraphRepoTargets(cfg *config.SystemConfig, repoPaths map[string]string) []CodeGraphRepoTarget
func PrepareCodeGraphIndexes(ctx context.Context, opts CodeGraphIndexOptions) CodeGraphIndexReport
func InvalidateCodeGraphIndexCache(systemID string)
```

Status values are `ready`, `skipped`, `warn`; actions are `initialized`, `synced`, `skipped`, `failed`.

- [ ] **Step 1: Write a fake CLI runner and failing lifecycle tests**

Inject command execution through:

```go
type codeGraphCommandRunner func(ctx context.Context, binary string, args ...string) ([]byte, error)
var runCodeGraphCommand codeGraphCommandRunner = runCodeGraphCommandExec
```

Tests must assert exact command order and results in these named cases: `TestPrepareCodeGraphIndexes_InitializesMissingIndex`, `TestPrepareCodeGraphIndexes_SyncsExistingIndex`, `TestPrepareCodeGraphIndexes_SkipsMissingPathAndNoSource`, `TestPrepareCodeGraphIndexes_TimeoutAndPartialFailureDoNotStopPeers`, `TestPrepareCodeGraphIndexes_MaxConcurrencyTwo`, `TestPrepareCodeGraphIndexes_ProcessCacheReusesReport`, and `TestBuildCodeGraphRepoTargets_AnalysisEnabledOnlyAndAbsolutePaths`. The timeout test blocks one fake command until its context expires while its peer returns complete; the concurrency test increments/decrements an atomic counter around every fake call and asserts its maximum equals 2; the cache test counts commands and asserts the second identical request adds zero.

The fake `status` JSON must use the real v1.3.1 shape:

```json
{"initialized":true,"version":"1.3.1","projectPath":"/repo","fileCount":12,"nodeCount":84,"edgeCount":103,"index":{"builtWithVersion":"1.3.1","builtWithExtractionVersion":51,"currentExtractionVersion":51,"reindexRecommended":false,"state":"complete","pendingRefs":0}}
```

- [ ] **Step 2: Run focused tests and verify manager symbols are missing**

Run: `go test ./internal/agent/ -run 'TestPrepareCodeGraphIndexes|TestBuildCodeGraphRepoTargets'`

Expected: FAIL with undefined types/functions.

- [ ] **Step 3: Implement target selection, source detection, and git HEAD capture**

`BuildCodeGraphRepoTargets` must:

- iterate config order;
- include only `repo.Analysis.Enabled`;
- trim and `filepath.Abs` the supplied local path;
- keep a target with an empty path so the report can expose a visible skip;
- use `exec.Command("git", "-C", absPath, "rev-parse", "HEAD")`; leave `Head` empty if the directory is not a git checkout;
- deduplicate identical absolute paths while retaining the first repository name, because monorepo entries share one `.codegraph` index.

Source detection must skip `.git`, `.codegraph`, `node_modules`, `vendor`, `dist`, `build`, and use the exact pinned v1.3.1 extension set from `src/extraction/grammars.ts`, including `.ts/.tsx/.mts/.cts/.ets/.js/.mjs/.cjs/.xsjs/.xsjslib/.jsx/.py/.pyw/.go/.rs/.java/.c/.h/.cpp/.cc/.cxx/.hpp/.hxx/.cs/.cshtml/.razor/.php/.module/.install/.theme/.inc/.yml/.yaml/.twig/.rb/.rake/.swift/.kt/.kts/.dart/.liquid/.svelte/.vue/.astro/.r/.pas/.dpr/.dpk/.lpr/.dfm/.fmx/.scala/.sc/.lua/.luau/.m/.mm/.sol/.cfc/.cfm/.cfs/.metal/.cu/.cuh/.nix/.xml/.cbl/.cob/.cobol/.cpy/.vb/.erl/.hrl/.escript/.properties/.tf/.tfvars/.tofu`; stop at the first match.

- [ ] **Step 4: Implement lifecycle, limits, deterministic reporting, and cache**

For each repository:

```text
[]string{"status", target.Path, "--json"}
  initialized=false -> []string{"init", target.Path} with 120s timeout -> status again
  initialized=true  -> []string{"sync", target.Path} with 30s timeout -> status again
```

Apply default timeouts/concurrency only when option values are zero. Use a buffered semaphore of size 2, preserve input ordering by writing to a preallocated result slice, and compute `Ready` only for final `initialized=true`, `index.state=complete`, `fileCount>0`, `nodeCount>0` results.

Cache under a mutex with exact key:

```go
systemID + "\n" + strings.Join(sorted([]string{name + "=" + absPath + "@" + head}), "\n")
```

Cache both successful and warning reports for the process; return a deep copy so callers cannot mutate cached slices. `InvalidateCodeGraphIndexCache(systemID)` removes all entries with that system prefix and is used by the retry binding.

Progress lines must use these forms:

```text
[codegraph] order-service: initialized (812 files, 9420 nodes, 3.2s)
[codegraph] user-service: synced (812 files, 9431 nodes, 0.8s)
[codegraph] docs: skipped (no supported source files)
[codegraph] legacy: warn (sync timeout, fallback enabled)
```

- [ ] **Step 5: Run index tests with the race detector**

Run: `go test ./internal/agent/ -race -run 'TestPrepareCodeGraphIndexes|TestBuildCodeGraphRepoTargets'`

Expected: PASS; measured fake-runner concurrency never exceeds 2, one repository timeout leaves its peer `ready`, and a second identical call executes no commands.

- [ ] **Step 6: Commit the index manager**

```bash
git add internal/agent/codegraph_index.go internal/agent/codegraph_index_test.go
git commit -m "feat: manage bounded CodeGraph repository indexes"
```

---

### Task 5: Register one shared CodeGraph MCP across all targets

**Files:**
- Create: `internal/agent/install_native_mcp_code_intelligence.go`
- Modify: `internal/agent/install_native_mcp_common.go` (`MCPBuildOptions`, dispatch)
- Modify: `internal/agent/install_native_mcp_common_test.go`
- Modify: `internal/agent/install_native_mcp.go` (IDE ensure + option)
- Modify: `internal/agent/install_native_openclaw_mcp.go` (OpenClaw ensure + option)
- Modify: `internal/agent/self_test_openclaw_probes.go` (`requiredMCPKeys`)
- Modify: `internal/agent/self_test_openclaw_test.go`

**Interfaces:**
- Consumes: `CfgUsesCodeGraph`, `EnsureCodeGraphInstalled`.
- Produces: `MCPBuildOptions.CodeGraphBinaryPath string`; builder key `b.keyFixed("codegraph")`.

- [ ] **Step 1: Add positive, negative, and required-key tests**

Add exact assertions:

```go
func TestBuildMCPServers_CodeGraph(t *testing.T) {
    cfg := &config.SystemConfig{
        System: config.System{ID: "shop"},
        CodeIntelligence: config.CodeIntelligence{Enabled: true, Provider: "codegraph"},
    }
    got := BuildMCPServers(cfg, MCPBuildOptions{AgentID: "shop", CodeGraphBinaryPath: "/opt/tshoot/codegraph"}, func(string) string { return "" })
    srv := got["shop-codegraph"].(map[string]any)
    if srv["command"] != "/opt/tshoot/codegraph" { t.Fatalf("%#v", srv) }
    if diff := cmp.Diff([]any{"serve", "--mcp"}, srv["args"]); diff != "" { t.Fatal(diff) }
    env := srv["env"].(map[string]any)
    if env["CODEGRAPH_TELEMETRY"] != "0" || env["DO_NOT_TRACK"] != "1" { t.Fatalf("%#v", env) }
    if _, exists := env["CODEGRAPH_MCP_TOOLS"]; exists { t.Fatal("must retain default one-tool surface") }
}

func TestBuildMCPServers_CodeGraphDisabledOrMissingBinary(t *testing.T) {
    for _, tc := range []struct{ enabled bool; binary string }{{false, "/opt/tshoot/codegraph"}, {true, ""}} {
        cfg := &config.SystemConfig{System: config.System{ID: "shop"}}
        if tc.enabled { cfg.CodeIntelligence = config.CodeIntelligence{Enabled: true, Provider: "codegraph"} }
        got := BuildMCPServers(cfg, MCPBuildOptions{AgentID: "shop", CodeGraphBinaryPath: tc.binary}, func(string) string { return "" })
        if _, exists := got["shop-codegraph"]; exists { t.Fatalf("unexpected server: %#v", got) }
    }
}

func TestRequiredMCPKeys_CodeGraph(t *testing.T) {
    cfg := &config.SystemConfig{CodeIntelligence: config.CodeIntelligence{Enabled: true, Provider: "codegraph"}}
    if got := requiredMCPKeys(cfg, "shop"); !slices.Contains(got, "shop-codegraph") { t.Fatalf("%v", got) }
}
```

The negative table has `(enabled=false, binary set)` and `(enabled=true, binary empty)`; neither may register a server.

- [ ] **Step 2: Run focused builder tests and observe missing server**

Run: `go test ./internal/agent/ -run 'TestBuildMCPServers_CodeGraph|TestRequiredMCPKeys_CodeGraph'`

Expected: FAIL because the builder option and dispatch are absent.

- [ ] **Step 3: Add the focused builder file and shared dispatch**

Implement:

```go
func (b *mcpBuilder) buildCodeGraph(servers map[string]any) {
    if !b.cfg.CodeIntelligence.UsesCodeGraph() || b.opts.CodeGraphBinaryPath == "" {
        return
    }
    servers[b.keyFixed("codegraph")] = map[string]any{
        "command": b.opts.CodeGraphBinaryPath,
        "args": []any{"serve", "--mcp"},
        "env": b.envBlock(map[string]any{
            "CODEGRAPH_TELEMETRY": "0",
            "DO_NOT_TRACK": "1",
        }),
    }
}
```

Add `CodeGraphBinaryPath string` to `MCPBuildOptions`, call `b.buildCodeGraph(servers)` once from `BuildMCPServers`, and append `withAgent("codegraph")` in `requiredMCPKeys` when `UsesCodeGraph()` is true.

- [ ] **Step 4: Ensure the bundle before both IDE and OpenClaw builds**

In both installer paths:

```go
codeGraphBinPath := ""
if CfgUsesCodeGraph(cfg) {
    var err error
    codeGraphBinPath, err = EnsureCodeGraphInstalled(emit)
    if err != nil {
        emit(fmt.Sprintf("[warn] CodeGraph 安装失败,跳过 MCP 注册并启用 rg/read fallback: %v", err))
    }
}
```

For OpenClaw, define an `emit` closure that writes to stderr before all ensure calls, replacing repeated anonymous logging closures. Pass the path in `MCPBuildOptions`. Do not return the ensure error from either deployment path.

- [ ] **Step 5: Run MCP builder and install regression tests**

Run: `go test ./internal/agent/ -run 'TestBuildMCPServers|TestRequiredMCPKeys|TestInstallNative'`

Expected: PASS; disabled config produces no CodeGraph download call and no server.

- [ ] **Step 6: Commit shared MCP registration**

```bash
git add internal/agent/install_native_mcp_code_intelligence.go internal/agent/install_native_mcp_common.go internal/agent/install_native_mcp_common_test.go internal/agent/install_native_mcp.go internal/agent/install_native_openclaw_mcp.go internal/agent/self_test_openclaw_probes.go internal/agent/self_test_openclaw_test.go
git commit -m "feat: register one shared CodeGraph MCP"
```

---

### Task 6: Orchestrate opt-in indexing during deployment and expose retry

**Files:**
- Modify: `internal/agent/apply.go`
- Modify: `internal/agent/apply_test.go`
- Modify: `cmd/tshoot-desktop/bindings_apply.go`
- Create: `cmd/tshoot-desktop/bindings_codegraph.go`
- Create: `cmd/tshoot-desktop/bindings_codegraph_test.go`

**Interfaces:**
- Consumes: Task 3 installer and Task 4 index manager.
- Produces:

```go
type Result struct {
    // existing fields remain unchanged
    CodeGraph *CodeGraphIndexReport `json:"codegraph,omitempty"`
}

func PrepareCodeGraphForDeploy(ctx context.Context, cfg *config.SystemConfig, repoPaths map[string]string, onLog func(string)) *CodeGraphIndexReport

func (a *App) ReindexCodeGraph(yamlText string, repoPaths map[string]string) (*agent.CodeGraphIndexReport, error)
```

- [ ] **Step 1: Add failing deploy orchestration tests**

Inject installer/preparer functions in `apply.go` through package variables so tests never download:

```go
var ensureCodeGraphForDeploy = EnsureCodeGraphInstalled
var prepareCodeGraphForDeploy = PrepareCodeGraphIndexes
```

Add four named tests: `TestApply_CodeGraphDisabledDoesNothing` makes both injected functions fail the test if called; `TestApply_CodeGraphFailureWarnsAndStillDeploys` returns `checksum mismatch` from ensure and asserts a successful result plus fallback log; `TestApply_CodeGraphReportReturned` returns a one-ready-repository fake report and compares the JSON fields; `TestImportAndApply_MultiTargetCacheAvoidsSecondIndex` calls two target deployments with the same system/path/HEAD and asserts the preparer command counter is unchanged on the second call.

The failure test makes ensure return `errors.New("checksum mismatch")`, records `OnLog`, and still asserts non-nil successful `Result` with a warning line containing `rg/read fallback`.

- [ ] **Step 2: Run focused apply tests and verify report/orchestrator are absent**

Run: `go test ./internal/agent/ -run 'TestApply_CodeGraph|TestImportAndApply_MultiTargetCache'`

Expected: FAIL because `Result.CodeGraph` and orchestration do not exist.

- [ ] **Step 3: Add deploy preparation without changing dry-run behavior**

Implement `PrepareCodeGraphForDeploy` to:

```go
if !cfg.CodeIntelligence.UsesCodeGraph() { return nil }
binary, err := ensureCodeGraphForDeploy(onLog)
if err != nil {
    log("[codegraph] warn: binary unavailable; rg/read fallback enabled: " + err.Error())
    return &CodeGraphIndexReport{Total: len(BuildCodeGraphRepoTargets(cfg, repoPaths))}
}
report := prepareCodeGraphForDeploy(ctx, CodeGraphIndexOptions{
    BinaryPath: binary,
    SystemID: cfg.System.ID,
    Repos: BuildCodeGraphRepoTargets(cfg, repoPaths),
    OnProgress: onLog,
    InitTimeout: 120 * time.Second,
    SyncTimeout: 30 * time.Second,
    MaxConcurrency: 2,
})
return &report
```

Use `context.Background()` in current non-context `Apply`; use the same helper in the OpenClaw branch of `ImportAndApply`. Do not call it during `DryRun`. Attach the report to every returned `Result`. Process cache from Task 4 prevents repeated work when the wizard loops over targets.

- [ ] **Step 4: Add the desktop retry binding**

`ReindexCodeGraph` must parse/validate YAML, expand `~` in paths with `userconfig.ExpandHome`, require `UsesCodeGraph()`, invalidate the system cache, ensure the binary, run the manager, and emit the same `install:log` event with prefix `[codegraph-retry]`. Installer/index errors are returned from this explicit retry action because the user asked for a result; per-repository failures remain inside the report.

Test disabled config rejection, path expansion, cache invalidation, and JSON serialization of `ready`, `total`, and per-repo counts.

- [ ] **Step 5: Run backend and desktop binding tests**

Run: `go test ./internal/agent/ ./cmd/tshoot-desktop/ -run 'TestApply_CodeGraph|TestImportAndApply_MultiTargetCache|TestReindexCodeGraph'`

Expected: PASS; an index warning never changes deployment success.

- [ ] **Step 6: Commit deployment orchestration**

```bash
git add internal/agent/apply.go internal/agent/apply_test.go cmd/tshoot-desktop/bindings_apply.go cmd/tshoot-desktop/bindings_codegraph.go cmd/tshoot-desktop/bindings_codegraph_test.go
git commit -m "feat: preindex CodeGraph repositories during deploy"
```

---

### Task 7: Extend self-test with real tool-name and index-health probes

**Files:**
- Create: `internal/agent/self_test_codegraph.go`
- Create: `internal/agent/self_test_codegraph_test.go`
- Modify: `internal/agent/self_test_mcp_probe.go`
- Modify: `internal/agent/self_test_mcp_probe_test.go`
- Modify: `internal/agent/self_test_openclaw.go`
- Modify: `internal/agent/self_test_openclaw_test.go`

**Interfaces:**
- Consumes: CodeGraph server spec in installed `openclaw.json`, generated `repo-path-map.yaml`, real `status --json` shape.
- Produces:

```go
func probeCodeGraphIndexes(ctx context.Context, cfg *config.SystemConfig, workspaceDir string, binary string, add func(name, status, detail string))
```

- [ ] **Step 1: Add failing MCP tool-name and index-status tests**

Add MCP probe cases `TestProbeMCPServers_CodeGraphRequiresExploreTool` and `TestProbeMCPServers_CodeGraphExploreToolPasses`.

The first fake returns `tools/list` with `codegraph_status` only and expects FAIL containing `codegraph_explore`; the second returns `codegraph_explore` and expects PASS.

Add index cases `TestProbeCodeGraphIndexes_CompleteIndexPasses`, `TestProbeCodeGraphIndexes_MissingIndexWarnsSeparatelyFromMCP`, `TestProbeCodeGraphIndexes_StaleExtractionWarnsRebuild`, and `TestProbeCodeGraphIndexes_NoSupportedSourceSkipsZeroNodes`. Each creates a real temporary `repo-path-map.yaml`, stubs only `runCodeGraphCommand`, records `SelfTestCheck` values through `add`, and compares exact status/detail substrings.

- [ ] **Step 2: Run focused self-test cases and observe missing assertions**

Run: `go test ./internal/agent/ -run 'TestProbeMCPServers_CodeGraph|TestProbeCodeGraphIndexes'`

Expected: FAIL because generic MCP probe accepts any non-empty tool list and index probe is undefined.

- [ ] **Step 3: Require the runtime-observed tool name for CodeGraph only**

After generic `tools/list` succeeds, detect CodeGraph by server key suffix `-codegraph` or command base `codegraph[.cmd]`. If `ProbeResult.Tools` lacks `codegraph_explore`, add FAIL:

```text
MCP %s tool surface: expected codegraph_explore, got %s
```

Do not hardcode expected tool names for unrelated servers.

- [ ] **Step 4: Parse generated repo paths and probe indexes separately**

Read:

```text
filepath.Join(workspaceDir, "skills", "routing", "references", "repo-path-map.yaml")
```

with this small YAML struct:

```go
type generatedRepoPathMap struct {
    Repos map[string]struct {
        LocalPath string `yaml:"local_path"`
    } `yaml:"repos"`
}
```

For each `analysis.enabled` repo:

- absent path: `WARN`;
- no supported source: `SKIP` even if status reports zero nodes;
- command failure/uninitialized/non-complete state: `WARN`, not global FAIL;
- complete index requires `fileCount>0` and `nodeCount>0`;
- `index.reindexRecommended=true` or `builtWithExtractionVersion < currentExtractionVersion`: `WARN` with `重新索引`;
- healthy: `PASS` with files/nodes/edges.

Reuse the source detector and status parser from `codegraph_index.go`; do not duplicate the v1.3.1 JSON struct.

Extract the binary command from `servers[mcpPrefix+"-codegraph"].command`. Invoke `probeCodeGraphIndexes` immediately after `probeMCPServersFromConfig`, so UI shows “server 可启动” and “仓库索引可查询” as separate rows.

- [ ] **Step 5: Run self-test regression suite**

Run: `go test ./internal/agent/ -run 'TestSelfTestOpenclaw|TestProbeMCPServers_CodeGraph|TestProbeCodeGraphIndexes'`

Expected: PASS; missing index adds WARN but does not set `SelfTestResult.OK=false`, while missing `codegraph_explore` remains a FAIL.

- [ ] **Step 6: Commit dual-layer self-test**

```bash
git add internal/agent/self_test_codegraph.go internal/agent/self_test_codegraph_test.go internal/agent/self_test_mcp_probe.go internal/agent/self_test_mcp_probe_test.go internal/agent/self_test_openclaw.go internal/agent/self_test_openclaw_test.go
git commit -m "feat: probe CodeGraph MCP and indexes separately"
```

---

### Task 8: Add wizard YAML/draft/import round-trip support

**Files:**
- Modify: `web/src/lib/yamlGenerator.ts`
- Modify: `web/src/lib/yamlGenerator.test.ts`
- Modify: `web/src/lib/yamlImporter.ts`
- Modify: `web/src/lib/yamlImporter.test.ts`
- Modify: `web/src/lib/useWizardDraft.ts`
- Modify: `web/src/pages/InitPage.vue` (reactive state, autosave, context wiring, skill derivation)

**Interfaces:**
- Consumes: Task 1 YAML shape.
- Produces TypeScript state:

```ts
export interface CodeIntelligenceState {
  enabled: boolean
  provider: 'codegraph'
}
```

Add `codeIntelligence: CodeIntelligenceState` to `YAMLGenContext`, `ApplyImportContext`, and `WizardDraft`.

- [ ] **Step 1: Add failing YAML generation/import tests**

Extend `makeCtx` with disabled state and add:

```ts
it('omits code_intelligence by default', () => {
  expect(generateYAML(makeCtx())).not.toContain('code_intelligence:')
})

it('emits enabled codegraph and its skill', () => {
  const ctx = makeCtx({ codeIntelligence: { enabled: true, provider: 'codegraph' } })
  ctx.deriveSkillsWhitelist = () => ['routing', 'incident-investigator', 'code-intelligence-query']
  expect(generateYAML(ctx)).toContain('code_intelligence:\n  enabled: true\n  provider: codegraph')
})

it('imports code intelligence and defaults old YAML to disabled', async () => {
  const ctx = makeImportCtx({ codeIntelligence: { enabled: false, provider: 'codegraph' } })
  await applyParsedYAMLToWizardState({ code_intelligence: { enabled: true, provider: 'codegraph' } }, ctx)
  expect(ctx.codeIntelligence).toEqual({ enabled: true, provider: 'codegraph' })
  await applyParsedYAMLToWizardState({}, ctx)
  expect(ctx.codeIntelligence).toEqual({ enabled: false, provider: 'codegraph' })
})
```

- [ ] **Step 2: Run Vitest and verify the new context field is missing**

Run: `cd web && npx vitest run src/lib/yamlGenerator.test.ts src/lib/yamlImporter.test.ts --pool=forks`

Expected: FAIL type-check/runtime assertions for absent `codeIntelligence` support.

- [ ] **Step 3: Emit and import the YAML contract**

In `generateYAML`, after repositories and before infrastructure, emit only when enabled:

```ts
if (ctx.codeIntelligence.enabled) {
  lines.push('')
  lines.push('code_intelligence:')
  lines.push('  enabled: true')
  lines.push('  provider: codegraph')
}
```

In importer, assign enabled only when parsed `enabled === true` and provider is absent or `codegraph`; always normalize provider to `codegraph`. Go validation remains authoritative for invalid imported YAML.

- [ ] **Step 4: Wire InitPage state, autosave, generator, importer, and whitelist**

Initialize:

```ts
const codeIntelligence = reactive<CodeIntelligenceState>({
  enabled: saved?.codeIntelligence?.enabled ?? false,
  provider: 'codegraph',
})
```

Add it to the deep autosave object, `buildContext()` for YAML import, and `YAMLGenContext`. In `deriveSkillsWhitelist`, append `code-intelligence-query` when enabled, after `incident-investigator`; deduplicate using the function’s existing Set/order convention.

- [ ] **Step 5: Run front-end unit and type tests**

Run: `cd web && npx vitest run src/lib/yamlGenerator.test.ts src/lib/yamlImporter.test.ts --pool=forks && npm run build`

Expected: PASS; old draft/YAML remains disabled and enabled YAML round-trips without losing the provider.

- [ ] **Step 6: Commit wizard data flow**

```bash
git add web/src/lib/yamlGenerator.ts web/src/lib/yamlGenerator.test.ts web/src/lib/yamlImporter.ts web/src/lib/yamlImporter.test.ts web/src/lib/useWizardDraft.ts web/src/pages/InitPage.vue
git commit -m "feat: round-trip CodeGraph wizard configuration"
```

---

### Task 9: Add the opt-in control, progress summary, and retry UI

**Files:**
- Create: `web/src/components/CodeIntelligenceToggle.vue`
- Create: `web/src/components/CodeIntelligenceToggle.test.ts`
- Modify: `web/src/components/OneClickDeployStep.vue`
- Create: `web/src/components/OneClickDeployStep.test.ts`
- Modify: `web/src/lib/bridge/install.ts`
- Modify: `web/src/lib/useDeployFlow.ts`
- Modify: `web/src/pages/InitPage.vue` (component placement and props/events)
- Regenerate: `web/wailsjs/go/main/App.d.ts`
- Regenerate: `web/wailsjs/go/main/App.js`
- Regenerate: `web/wailsjs/go/models.ts`

**Interfaces:**
- Consumes: backend `agent.Result.CodeGraph` and `App.ReindexCodeGraph`.
- Produces:

```ts
export type CodeGraphRepoResult = {
  name: string
  path: string
  action: 'initialized' | 'synced' | 'skipped' | 'failed'
  status: 'ready' | 'skipped' | 'warn'
  detail?: string
  file_count?: number
  node_count?: number
  edge_count?: number
  duration_ms: number
}

export type CodeGraphIndexReport = { ready: number; total: number; repos: CodeGraphRepoResult[] }
```

- [ ] **Step 1: Write focused component tests**

The toggle test must assert default unchecked, emitted `update:modelValue`, and all four disclosures: `约 200 MB+`, `.codegraph`, `仅存本机/telemetry 关闭`, `失败回退`.

The deploy-step test must mount with a report `{ready: 2,total: 3,...}` and assert `CodeGraph 2/3 repos ready`, the failed repo reason, and a retry event carrying no hidden path value.

- [ ] **Step 2: Run the component tests and verify components/props are missing**

Run: `cd web && npx vitest run src/components/CodeIntelligenceToggle.test.ts src/components/OneClickDeployStep.test.ts --pool=forks`

Expected: FAIL because the new component and report UI do not exist.

- [ ] **Step 3: Implement the Step 4 opt-in control**

Place the toggle after the repository list, because it applies to all analysis-enabled repos. Use `v-model` and render this exact user contract:

```text
启用 CodeGraph 代码智能
首次会下载约 200 MB+ 的本地工具，并在可分析仓库创建或更新 .codegraph/。
索引仅存本机；Studio 会关闭 CodeGraph telemetry。失败不影响部署，机器人会回退到 git diff + rg + Read。
```

Do not disable the control when a repository path is empty; deployment reports that repository as skipped.

- [ ] **Step 4: Capture one shared report while deploying multiple targets**

Change `importAndDeploy` typing to expose `codegraph`. In `useDeployFlow`, initialize `const codeGraphReport = ref<CodeGraphIndexReport | null>(null)`, and for each target:

```ts
const applied = await importAndDeploy(...)
if (!codeGraphReport.value && applied.codegraph) codeGraphReport.value = applied.codegraph
```

Return the report/ref from the composable and pass it to `OneClickDeployStep`. Process-level cache guarantees later target reports describe the same index run.

- [ ] **Step 5: Implement summary, repository details, and retry**

When a report exists, show:

```text
CodeGraph ${report.ready}/${report.total} repos ready
```

Render each repo with ready/skipped/warn state, action, file/node counts, and detail. Show `重新索引失败仓库` only when `ready < total`. Bridge function:

```ts
export async function reindexCodeGraph(
  yamlText: string,
  repoPaths: Record<string, string>,
): Promise<CodeGraphIndexReport> {
  if (!isDesktop()) throw new Error('ReindexCodeGraph 只在桌面 app 里可用')
  return App.ReindexCodeGraph(yamlText, repoPaths) as unknown as CodeGraphIndexReport
}
```

Reuse the exact repo-path construction already in `useDeployFlow` by extracting it into a local `buildDeployRepoPaths()` function; do not create a second path-resolution implementation. Retry updates the same ref and sends progress through existing `install:log`.

- [ ] **Step 6: Regenerate Wails bindings and run front-end verification**

Run:

```bash
make wails-gen
cd web
npx vitest run src/components/CodeIntelligenceToggle.test.ts src/components/OneClickDeployStep.test.ts src/lib/yamlGenerator.test.ts src/lib/yamlImporter.test.ts --pool=forks
npm run build
```

Expected: Wails declarations contain `ReindexCodeGraph`; all tests and `vue-tsc` pass.

- [ ] **Step 7: Commit the user-facing control and feedback**

```bash
git add web/src/components/CodeIntelligenceToggle.vue web/src/components/CodeIntelligenceToggle.test.ts web/src/components/OneClickDeployStep.vue web/src/components/OneClickDeployStep.test.ts web/src/lib/bridge/install.ts web/src/lib/useDeployFlow.ts web/src/pages/InitPage.vue web/wailsjs/go/main/App.d.ts web/wailsjs/go/main/App.js web/wailsjs/go/models.ts
git commit -m "feat: expose CodeGraph opt-in and index status"
```

---

### Task 10: Add a pinned live smoke test for CLI, MCP, query, freshness, and branch behavior

**Files:**
- Create: `internal/agent/codegraph_live_test.go`
- Create: `scripts/test-codegraph-smoke.sh`

**Interfaces:**
- Consumes: `EnsureCodeGraphInstalled`, existing MCP stdio probe helpers, v1.3.1 CLI.
- Produces: opt-in command `scripts/test-codegraph-smoke.sh`; default test suite remains offline.

- [ ] **Step 1: Write the env-gated live Go test**

The test starts with:

```go
if os.Getenv("TSHOOT_CODEGRAPH_LIVE") != "1" {
    t.Skip("set TSHOOT_CODEGRAPH_LIVE=1 to download/run pinned CodeGraph")
}
```

It must create one temporary Go repository and one Java repository, initialize git branches (`main` and `release/prod`), write connected symbols with unique names, then assert:

1. `init` yields complete status and non-zero files/nodes;
2. `serve --mcp` initialize/tools-list contains `codegraph_explore`;
3. `codegraph_explore` with explicit `projectPath` returns the unique symbol and a source line;
4. adding a new symbol then running `sync` makes it queryable;
5. a never-initialized repository produces the manager fallback warning;
6. the branch comparison helper reports mismatch and never changes the checked-out branch.

Always remove temp repositories; do not remove the shared downloaded bundle.

- [ ] **Step 2: Add the wrapper script**

Use:

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
TSHOOT_CODEGRAPH_LIVE=1 go test ./internal/agent/ -run TestCodeGraphLive -count=1 -v
```

Make it executable. The script must not call `latest` or install CodeGraph globally.

- [ ] **Step 3: Verify default tests skip and the explicit smoke test passes**

Run: `go test ./internal/agent/ -run TestCodeGraphLive -v`

Expected: SKIP without downloading.

Run: `scripts/test-codegraph-smoke.sh`

Expected: PASS with a runtime-observed `codegraph_explore`; first run may download the verified v1.3.1 bundle and take several minutes.

- [ ] **Step 4: Commit the smoke test**

```bash
git add internal/agent/codegraph_live_test.go scripts/test-codegraph-smoke.sh
git commit -m "test: add live CodeGraph MCP smoke coverage"
```

---

### Task 11: Document the operating model and append the ADR

**Files:**
- Modify: `README.md`
- Modify: `CONTRIBUTING.md`
- Modify: `docs/decisions.md`

**Interfaces:**
- Consumes: final config, commands, limits, and fallback behavior from Tasks 1–10.
- Produces: operator and contributor documentation; no runtime interface.

- [ ] **Step 1: Add README user documentation**

Document this opt-in example:

```yaml
code_intelligence:
  enabled: true
  provider: codegraph
```

State all of: roughly 200 MB+ bundle, per-repo `.codegraph/`, local-only index, telemetry disabled, fixed version/SHA verification, one shared MCP, explicit `projectPath`, no automatic checkout, low confidence on branch mismatch, `rg/read` fallback, and uninstall retention.

- [ ] **Step 2: Extend CONTRIBUTING MCP requirements**

Add CodeGraph-specific checks to “加 MCP 接入” and “测试要求”:

```text
- 固定 release version + per-platform SHA256；升级时先跑 scripts/test-codegraph-smoke.sh。
- runtime probe 必须确认 tools/list 含 codegraph_explore。
- index probe 与 MCP probe 分开；含源码仓库要求 complete/fileCount/nodeCount。
- 普通单测使用 fake CLI，禁止隐式联网下载。
```

- [ ] **Step 3: Append, never rewrite, a decisions ADR**

Append an ADR titled `CodeGraph 作为可选故障期代码图谱，保留 analyzer 主路径` recording:

- context: regex analyzer lacks symbol/call/impact evidence; CodeGraph is Tree-sitter/SQLite/MCP rather than LSP;
- decision: opt-in, one shared MCP + `projectPath`, fixed v1.3.1/SHA, pre-query sync, dual self-test, telemetry off;
- rejected: per-repo MCP, embedding into analyzer, auto checkout/worktrees, enabling hidden tools;
- consequences: 200 MB+ disk cost, `.codegraph` lifecycle, heuristic graph edges, stable fallback and upgrade procedure.

- [ ] **Step 4: Check documentation for contract drift**

Run:

```bash
rg -n "code_intelligence|codegraph_explore|CODEGRAPH_TELEMETRY|200 MB|rg/read" README.md CONTRIBUTING.md docs/decisions.md
rg -n "latest|codegraph install|CODEGRAPH_MCP_TOOLS" README.md CONTRIBUTING.md docs/decisions.md
```

Expected: the first command finds all contracts; the second has no instruction that enables `latest`, upstream installer mutation, or hidden tools.

- [ ] **Step 5: Commit documentation and ADR**

```bash
git add README.md CONTRIBUTING.md docs/decisions.md
git commit -m "docs: record CodeGraph integration and fallback model"
```

---

### Task 12: Run final cross-layer verification and inspect the exact diff

**Files:**
- Modify only files required to fix failures found by the commands below.

**Interfaces:**
- Consumes: all prior tasks.
- Produces: verified implementation satisfying the design acceptance criteria.

- [ ] **Step 1: Format generated and handwritten code**

Run:

```bash
gofmt -w internal/config/types_code_intelligence.go internal/config/code_intelligence_test.go internal/agent/ensure_codegraph.go internal/agent/ensure_codegraph_test.go internal/agent/codegraph_index.go internal/agent/codegraph_index_test.go internal/agent/install_native_mcp_code_intelligence.go internal/agent/self_test_codegraph.go internal/agent/self_test_codegraph_test.go internal/agent/codegraph_live_test.go cmd/tshoot-desktop/bindings_codegraph.go
```

Then run `git diff --check` and fix every whitespace error.

- [ ] **Step 2: Run the focused backend acceptance suite**

Run:

```bash
go test ./internal/config/ -run 'TestCodeIntelligence|TestHealthCheck_CodeIntelligence'
go test ./internal/agent/ -run 'TestBuildMCPServers|TestCodeGraph|TestEnsureCodeGraph|TestPrepareCodeGraph|TestSelfTestOpenclaw|TestProbeCodeGraph'
go test ./internal/generator/ -run 'TestGenerate|TestSkillScriptPathsExist'
go test ./api/
```

Expected: PASS, with the live test skipped unless explicitly enabled.

- [ ] **Step 3: Run template and front-end acceptance suites**

Run:

```bash
scripts/test-skill-scripts.sh
cd web
npm test
npm run build
cd ..
```

Expected: PASS with no missing skill paths or TypeScript binding drift.

- [ ] **Step 4: Run the mandatory full repository gates**

Run:

```bash
go test ./... -race
scripts/check-go-coverage.sh
make lint
make build
```

Expected: PASS; `internal/agent` remains at or above 60% and `internal/generator` at or above 65% coverage.

- [ ] **Step 5: Run the live release probe once before merge**

Run: `scripts/test-codegraph-smoke.sh`

Expected: PASS on a supported platform with `codegraph_explore` observed at runtime, Go and Java symbols queryable, and sync freshness verified.

- [ ] **Step 6: Audit acceptance criteria and unrelated changes**

Run:

```bash
git status --short
git diff --stat HEAD~11..HEAD
git diff --name-only HEAD~11..HEAD
git diff --check HEAD~11..HEAD
```

Manually verify:

1. disabled YAML triggers no download, index, skill, MCP, or UI report;
2. enabled YAML registers exactly one MCP and runtime tools include `codegraph_explore`;
3. every eligible repository is ready or visibly skipped/warned;
4. one repository failure leaves deployment and other repositories successful;
5. runtime skill always supplies routing-derived absolute `projectPath` and `maxFiles=4`;
6. branch mismatch never checks out and caps code confidence at low;
7. fallback commands remain present in generated incident-investigator;
8. self-test has distinct server and index rows;
9. repeated multi-target deploy uses the process cache;
10. telemetry variables and fixed SHA checks are present;
11. pre-existing unrelated worktree changes are not included in any CodeGraph commit.

- [ ] **Step 7: Fold any verification fixes into the owning task**

Run `git diff --name-only` and map each changed file back to Tasks 1–11. Stage it with that task's explicit `git add` list and amend the owning commit with `git commit --amend --no-edit`. If no files changed during verification, do not create an empty commit.
