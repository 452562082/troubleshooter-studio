# Validation Agent Rendered Browser Evidence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make validation and regression use the generated validator role plus a Studio-hosted Playwright browser that produces safely previewable rendered screenshots, sanitized network/console evidence, and bounded action traces.

**Architecture:** `AgentPhaseRunner` keeps ownership of the durable attempt and adds a browser-assisted coordinator for Web attempts: validator planning call → host `BrowserVerifier` execution → optional one-time locator repair → validator evaluation call. A new `internal/browserverify` package owns the pinned Playwright runtime, URL/action policy, encrypted browser sessions, worker process, crash journal, and evidence sanitization; Wails bindings adapt that host capability to the existing Case UI without storing credentials in Case JSON.

**Tech Stack:** Go 1.x, SQLite-backed `internal/bughub`, Node.js 20.19+ worker, Playwright `1.61.1` with Chromium, AES-GCM plus system keyring, Wails v2, Vue 3, TypeScript, Vitest.

## Global Constraints

- Use Playwright exactly `1.61.1`; install one shared runtime below the Studio management root, never in a generated bot workspace.
- Default tests must not download Playwright, start a real business browser, access a business environment, or modify a real login session.
- The real browser smoke test is opt-in through `TSHOOT_BROWSER_SMOKE=1` and uses only a local `httptest`/loopback page.
- Allowed browser actions are exactly `goto`, `click`, `fill`, `press`, `select`, `wait_for`, and `screenshot`; never expose `evaluate`, arbitrary JavaScript, browser extensions, file upload, XPath, or shell commands.
- For `is_prod=true`, automated execution allows only `goto`, `wait_for`, and `screenshot`.
- Permanently reject `file:`, `data:`, `javascript:`, browser-internal schemes, metadata destinations, unspecified, multicast, and link-local addresses; private IPs require an exact configured environment origin.
- Browser redirects and every network request must remain inside the origin allowlist and pass DNS/IP revalidation.
- Never persist raw Cookie, `Set-Cookie`, Authorization, password, raw HAR, raw request/response bodies, or Playwright `trace.zip`.
- A successful Web validation/regression requires a current-attempt final PNG; a failed browser execution requires a failure PNG unless a login/password page is visible.
- Login uses the existing `waiting_evidence` Case status plus `browser_login_required`; do not add a new Case status.
- Login credentials are entered only in a visible Studio-owned browser. Persist only AES-GCM-encrypted `storageState`; keep the AES key in the system keyring. If the keyring is unavailable, keep the session in memory only.
- Browser planning, one optional repair, and final evaluation remain one `PhaseAttempt`; aggregate usage across all Agent calls.
- Do not change fix-phase role behavior in this feature.
- Do not use `git add -A`; stage exact files and preserve all pre-existing worktree changes, especially `internal/webui/dist/.gitkeep`.
- Before Task 1, capture `git status --short` and `git diff` as the ownership baseline. If a task touches a file that was already modified (currently including `workflow_phase_runner.go`, its tests, and workflow documentation), use `git add -p -- <file>` and stage only the task's hunks; the full-file `git add` examples below apply only to files that were clean at the ownership baseline.

## File Structure

### Create

- `internal/bughub/workflow_phase_bot.go`: strict phase-to-execution-role resolution.
- `internal/bughub/workflow_phase_bot_test.go`: validator routing and missing-install coverage.
- `internal/bughub/workflow_browser_types.go`: BrowserPlan, policy, verifier interfaces, progress/result/error contracts.
- `internal/bughub/workflow_browser_types_test.go`: strict YAML and action matrix tests.
- `internal/bughub/workflow_browser_coordinator.go`: planner/worker/repair/evaluator orchestration and Web success evidence gate.
- `internal/bughub/workflow_browser_coordinator_test.go`: fake executor/verifier orchestration tests.
- `internal/browserverify/policy.go`: origin normalization, DNS/IP checks, production action policy.
- `internal/browserverify/policy_test.go`: SSRF, redirect, rebinding, and action-policy tests.
- `internal/browserverify/session.go`: encrypted storageState store with memory fallback.
- `internal/browserverify/session_test.go`: encryption, keyring failure, and clear tests.
- `internal/browserverify/runtime.go`: pinned runtime install/status/repair/probe.
- `internal/browserverify/runtime_test.go`: fake-command runtime lifecycle tests.
- `internal/browserverify/verifier.go`: worker protocol, journal, manifest, evidence and login orchestration.
- `internal/browserverify/verifier_test.go`: fake worker, crash recovery, idempotency, and redaction tests.
- `internal/browserverify/worker/sanitize.mjs`: Playwright-free URL/console/network sanitizers.
- `internal/browserverify/worker/browser_worker.mjs`: Playwright Chromium worker.
- `internal/browserverify/worker/browser_worker.test.mjs`: offline worker sanitizer/protocol tests.
- `cmd/tshoot-desktop/bindings_bug_browser.go`: browser policy adapter, login/repair/session/artifact bindings.
- `cmd/tshoot-desktop/bindings_bug_browser_test.go`: binding ownership, idempotency, and secret tests.
- `web/src/components/BugBrowserProgress.vue`: structured browser progress and recovery actions.
- `web/src/components/BugBrowserProgress.test.ts`: progress/login/runtime-error UI tests.
- `scripts/test-browser-worker.sh`: offline Node worker tests and opt-in real smoke.

### Modify

- `internal/config/types.go`, `internal/config/validate.go`, `internal/config/loader_test.go`: add and validate `browser_auth_origins`.
- `schema/troubleshooter.schema.yaml`, `examples/shop-troubleshooter.yaml`: document and fixture the auth-origin schema.
- `internal/bughub/workflow_phase_runner.go`, `internal/bughub/workflow_phase_runner_test.go`: execute derived validator, coordinate browser attempts, register host artifacts, aggregate usage, and expose stable errors.
- `internal/bughub/workflow_artifacts.go`, `internal/bughub/workflow_artifacts_test.go`: safe registered-artifact read API for preview/export.
- `internal/bughub/workflow_recovery.go`, `internal/bughub/workflow_recovery_test.go`: reconcile interrupted browser reservations without duplicate evidence.
- `cmd/tshoot-desktop/main.go`, `cmd/tshoot-desktop/bindings_bug_workflow.go`, `cmd/tshoot-desktop/bindings_bug_workflow_test.go`: own and inject one host browser controller.
- `web/src/lib/bridge/bugWorkflow.ts`, `web/src/lib/bridge/bugWorkflow.test.ts`: browser bindings and preview/result types.
- `web/src/lib/useIncidentCase.ts`, `web/src/lib/useIncidentCase.test.ts`: retain same-version live phase events.
- `web/src/components/BugCaseLifecycle.vue`, `web/src/components/BugCaseLifecycle.test.ts`: browser progress/login/runtime recovery actions.
- `web/src/components/BugCaseArtifacts.vue`, `web/src/components/BugCaseArtifacts.test.ts`: screenshot thumbnail/preview without filesystem paths.
- `web/src/pages/IncidentWorkbenchPage.vue`, `web/src/pages/IncidentWorkbenchPage.test.ts`: dispatch browser login, repair, clear-session, preview, and save actions.
- `web/wailsjs/go/main/App.d.ts`, `web/wailsjs/go/main/App.js`, `web/wailsjs/go/models.ts`: regenerated Wails bindings.
- `templates/workspace/skills/bug-verifier/SKILL.md.tmpl`, `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`: make Studio BrowserVerifier primary and the workspace script compatibility-only.
- `scripts/test-skill-scripts.sh`, `CONTRIBUTING.md`: add offline worker tests and opt-in smoke instructions.
- `docs/incident-workflow.md`, `docs/troubleshooting-flow.md`, `docs/decisions.md`: workflow and append-only ADR updates.

---

### Task 1: Route validation and regression to the installed validator role

**Files:**
- Create: `internal/bughub/workflow_phase_bot.go`
- Create: `internal/bughub/workflow_phase_bot_test.go`
- Modify: `internal/bughub/workflow_phase_runner.go`
- Modify: `internal/bughub/workflow_orchestrator.go`
- Test: `internal/bughub/workflow_phase_runner_test.go`
- Test: `internal/bughub/workflow_orchestrator_test.go`

**Interfaces:**
- Consumes: existing `ValidatorBotFor(BotRef) BotRef`, `Phase`, and persisted base `PhaseAttempt.BotKey`.
- Produces: `ExecutionBotForPhase(phase Phase, selected BotRef) (BotRef, error)` and `ErrValidatorNotInstalled`.

- [ ] **Step 1: Write the failing role-resolution tests**

```go
func TestExecutionBotForPhaseUsesValidatorForValidationAndRegression(t *testing.T) {
	root := t.TempDir()
	selectedPath := filepath.Join(root, "base-troubleshooter")
	validatorPath := filepath.Join(root, "base-validator")
	for _, path := range []string{selectedPath, validatorPath} {
		if err := os.MkdirAll(path, 0o755); err != nil { t.Fatal(err) }
	}
	selected := BotRef{
		Key: "base|codex", Target: "codex", Path: selectedPath,
		SystemID: "base", Role: "troubleshooter",
		InternalAgents: []BotInternalAgent{{ID: "base-validator", Role: "validator"}},
	}
	for _, phase := range []Phase{PhaseValidation, PhaseRegression} {
		got, err := ExecutionBotForPhase(phase, selected)
		if err != nil { t.Fatalf("phase %s: %v", phase, err) }
		if got.Role != "validator" || got.Path != validatorPath || got.Key != "base|codex#validator" {
			t.Fatalf("phase %s resolved %+v", phase, got)
		}
	}
	got, err := ExecutionBotForPhase(PhaseInvestigation, selected)
	if err != nil || got.Key != selected.Key || got.Path != selected.Path { t.Fatalf("investigation resolved %+v, %v", got, err) }
}

func TestExecutionBotForPhaseRejectsMissingValidator(t *testing.T) {
	selected := BotRef{Key: "base|codex", Target: "codex", Path: t.TempDir(), SystemID: "base", Role: "troubleshooter"}
	_, err := ExecutionBotForPhase(PhaseValidation, selected)
	if !errors.Is(err, ErrValidatorNotInstalled) { t.Fatalf("err = %v", err) }
}
```

- [ ] **Step 2: Run the focused tests and verify the new symbol is missing**

Run: `go test ./internal/bughub -run 'TestExecutionBotForPhase'`

Expected: FAIL with `undefined: ExecutionBotForPhase` and `undefined: ErrValidatorNotInstalled`.

- [ ] **Step 3: Add the strict resolver**

```go
var ErrValidatorNotInstalled = errors.New("validator role is not installed")

func ExecutionBotForPhase(phase Phase, selected BotRef) (BotRef, error) {
	switch phase {
	case PhaseInvestigation, PhaseFix:
		return selected, nil
	case PhaseValidation, PhaseRegression:
	default:
		return BotRef{}, fmt.Errorf("unsupported execution phase %q", phase)
	}
	if strings.EqualFold(strings.TrimSpace(selected.Role), "validator") {
		return selected, nil
	}
	validatorID := internalAgentIDForRole(selected, "validator")
	if validatorID == "" {
		return BotRef{}, fmt.Errorf("%w: selected bot %q has no validator agent metadata", ErrValidatorNotInstalled, selected.Key)
	}
	derived := ValidatorBotFor(selected)
	if strings.EqualFold(strings.TrimSpace(selected.Target), "openclaw") {
		if strings.TrimSpace(derived.AgentID) == "" { return BotRef{}, ErrValidatorNotInstalled }
		return derived, nil
	}
	if strings.TrimSpace(derived.Path) == "" || filepath.Clean(derived.Path) == filepath.Clean(selected.Path) {
		return BotRef{}, fmt.Errorf("%w: validator workspace %q is unavailable", ErrValidatorNotInstalled, validatorID)
	}
	info, err := os.Stat(derived.Path)
	if err != nil || !info.IsDir() {
		return BotRef{}, fmt.Errorf("%w: validator workspace %q is unavailable", ErrValidatorNotInstalled, derived.Path)
	}
	return derived, nil
}
```

- [ ] **Step 4: Derive the execution bot after persisted base-bot checks**

In `AgentPhaseRunner.Start`, keep all checks against `attempt.BotKey` and `incident.SelectedBotKey`, then insert:

```go
executionBot, err := ExecutionBotForPhase(attempt.Phase, bot)
if err != nil {
	return fail(err)
}
prompt, err := r.promptForAttempt(attempt, bug, executionBot)
```

Pass `executionBot` to `startLegacyProjection` and `run`, while leaving the persisted attempt and Case bot keys unchanged. In `phaseScheduleFailure`, persist a stable code instead of collapsing this case to `schedule_failed`:

```go
func phaseScheduleErrorCode(cause error) string {
	if errors.Is(cause, ErrValidatorNotInstalled) { return "validator_not_installed" }
	return "schedule_failed"
}
```

Use that code in both `attempt.ErrorCode` and the failure `OutputJSON`; sanitize the public message to `验证机器人未安装，请重新部署当前机器人` for this sentinel. Add an orchestrator assertion that the Case reaches `waiting_evidence` and its failed attempt carries `validator_not_installed`.

- [ ] **Step 5: Run routing and runner tests**

Run: `go test ./internal/bughub -run 'TestExecutionBotForPhase|TestAgentPhaseRunner'`

Expected: PASS; validation/regression executor calls contain the validator workspace, while investigation still contains the selected base workspace.

- [ ] **Step 6: Commit the role boundary**

```bash
git add internal/bughub/workflow_phase_bot.go internal/bughub/workflow_phase_bot_test.go internal/bughub/workflow_phase_runner.go internal/bughub/workflow_phase_runner_test.go internal/bughub/workflow_orchestrator.go internal/bughub/workflow_orchestrator_test.go
git commit -m "fix: route durable validation through validator role"
```

---

### Task 2: Define the strict BrowserPlan and configured origin contract

**Files:**
- Create: `internal/bughub/workflow_browser_types.go`
- Create: `internal/bughub/workflow_browser_types_test.go`
- Modify: `internal/config/types.go`
- Modify: `internal/config/validate.go`
- Modify: `internal/config/loader_test.go`
- Modify: `schema/troubleshooter.schema.yaml`
- Modify: `examples/shop-troubleshooter.yaml`

**Interfaces:**
- Consumes: strict YAML helper conventions already used by `ParseValidationResult`.
- Produces: `ParseBrowserPlan`, `BrowserVerifier`, `BrowserPolicyResolver`, `BrowserVerificationRequest`, `BrowserVerificationResult`, and `Environment.BrowserAuthOrigins`.

- [ ] **Step 1: Write failing strict-plan table tests**

```go
func TestParseBrowserPlanStrictlyValidatesActions(t *testing.T) {
	valid := []byte(`version: 1
start_url: https://test.example.com/users
actions:
  - id: open-users
    action: click
    locator: {kind: role, value: tab, name: 用户}
    screenshot_after: true
assertions:
  - kind: visible_text
    value: 汤圆
`)
	plan, err := ParseBrowserPlan(valid)
	if err != nil { t.Fatal(err) }
	if plan.Version != 1 || plan.Actions[0].ID != "open-users" { t.Fatalf("plan = %+v", plan) }

	cases := map[string]string{
		"unknown field": strings.Replace(string(valid), "version: 1", "version: 1\nevaluate: alert(1)" , 1),
		"unknown action": strings.Replace(string(valid), "action: click", "action: evaluate", 1),
		"xpath": strings.Replace(string(valid), "kind: role", "kind: xpath", 1),
		"duplicate id": strings.Replace(string(valid), "assertions:", "  - id: open-users\n    action: screenshot\nassertions:", 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBrowserPlan([]byte(raw)); err == nil { t.Fatal("expected strict validation error") }
		})
	}
}
```

Also add config tests that accept only absolute HTTP(S) origins without userinfo, path, query, or fragment:

```go
func TestValidateBrowserAuthOrigins(t *testing.T) {
	cfg := minimalValid()
	cfg.Environments[0].BrowserAuthOrigins = []string{"https://login.example.com"}
	if err := Validate(&cfg); err != nil { t.Fatal(err) }
	cfg.Environments[0].BrowserAuthOrigins = []string{"https://user:pass@login.example.com/path"}
	if err := Validate(&cfg); err == nil || !strings.Contains(err.Error(), "browser_auth_origins") { t.Fatalf("err = %v", err) }
}
```

- [ ] **Step 2: Verify the plan and config tests fail**

Run: `go test ./internal/bughub ./internal/config -run 'TestParseBrowserPlan|TestValidateBrowserAuthOrigins'`

Expected: FAIL because the protocol types and config field do not exist.

- [ ] **Step 3: Add the protocol types and strict decoder**

```go
type BrowserPlan struct {
	Version    int                `yaml:"version" json:"version"`
	StartURL   string             `yaml:"start_url" json:"start_url"`
	Actions    []BrowserAction    `yaml:"actions" json:"actions"`
	Assertions []BrowserAssertion `yaml:"assertions" json:"assertions"`
}
type BrowserLocator struct {
	Kind string `yaml:"kind" json:"kind"`
	Value string `yaml:"value" json:"value"`
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
}
type BrowserAction struct {
	ID string `yaml:"id" json:"id"`
	Action string `yaml:"action" json:"action"`
	Locator *BrowserLocator `yaml:"locator,omitempty" json:"locator,omitempty"`
	URL string `yaml:"url,omitempty" json:"url,omitempty"`
	Value string `yaml:"value,omitempty" json:"value,omitempty"`
	Key string `yaml:"key,omitempty" json:"key,omitempty"`
	ScreenshotAfter bool `yaml:"screenshot_after,omitempty" json:"screenshot_after,omitempty"`
}
type BrowserAssertion struct {
	Kind string `yaml:"kind" json:"kind"`
	Value string `yaml:"value" json:"value"`
}
type BrowserAccessibilityNode struct {
	Role string `json:"role"`
	Name string `json:"name"`
	Visible bool `json:"visible"`
	Disabled bool `json:"disabled"`
}

type BrowserVerificationRequest struct {
	CaseID string
	CycleNumber int
	AttemptID string
	SystemID string
	Environment string
	Version string
	Policy BrowserSecurityPolicy
	Plan BrowserPlan
	StagingDir string
	Emit func(BrowserProgress)
}
type BrowserSecurityPolicy struct {
	AllowedOrigins []string `json:"allowed_origins"`
	PrivateOrigins []string `json:"private_origins"`
	AuthOrigins []string `json:"auth_origins"`
	IsProd bool `json:"is_prod"`
}
type BrowserProgress struct { Code, Message, ActionID string; Current, Total int }
type BrowserArtifactReference struct { Kind, Path, Environment, Version, RequestID, TraceID string }
type BrowserVerificationResult struct {
	Status string
	ErrorCode string
	ErrorMessage string
	FailedActionID string
	FinalURL string
	Title string
	LoginOrigin string
	FinalScreenshotPath string
	AccessibilitySummary []BrowserAccessibilityNode
	Artifacts []BrowserArtifactReference
}
type BrowserVerifier interface {
	Execute(context.Context, BrowserVerificationRequest) (BrowserVerificationResult, error)
}
type BrowserPolicyResolver interface {
	ResolveBrowserPolicy(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error)
}
```

Implement `ParseBrowserPlan` with `yaml.Decoder.KnownFields(true)`, `version == 1`, 1–40 unique action IDs, the exact action/locator allowlists, action-specific required/forbidden fields, max 4096 bytes per string, and at least one assertion. Reject `goto` outside `start_url` until the host policy validates its explicit URL.

- [ ] **Step 4: Add `browser_auth_origins` to config and schema**

```go
type Environment struct {
	ID                     string                       `yaml:"id"`
	Aliases                []string                     `yaml:"aliases"`
	APIDomain              string                       `yaml:"api_domain"`
	WebDomain              string                       `yaml:"web_domain"`
	BrowserAuthOrigins     []string                     `yaml:"browser_auth_origins,omitempty"`
	IsProd                 bool                         `yaml:"is_prod"`
	DeploymentVerification DeploymentVerificationConfig `yaml:"deployment_verification,omitempty"`
}
```

Add `validateBrowserAuthOrigin` and call it for every configured value. Update the schema example and `examples/shop-troubleshooter.yaml` with:

```yaml
browser_auth_origins:
  - https://login.shop.example.com
```

- [ ] **Step 5: Run protocol, loader, and full config tests**

Run: `go test ./internal/bughub ./internal/config`

Expected: PASS, including unknown BrowserPlan fields and credential-bearing auth origins being rejected.

- [ ] **Step 6: Commit the protocol and schema**

```bash
git add internal/bughub/workflow_browser_types.go internal/bughub/workflow_browser_types_test.go internal/config/types.go internal/config/validate.go internal/config/loader_test.go schema/troubleshooter.schema.yaml examples/shop-troubleshooter.yaml
git commit -m "feat: define restricted browser validation protocol"
```

---

### Task 3: Enforce URL, DNS/IP, action, and evidence sanitization policy

**Files:**
- Create: `internal/browserverify/policy.go`
- Create: `internal/browserverify/policy_test.go`
- Create: `internal/browserverify/worker/sanitize.mjs`
- Create: `internal/browserverify/worker/browser_worker.test.mjs`

**Interfaces:**
- Consumes: `bughub.BrowserPlan` and `bughub.BrowserSecurityPolicy` from Task 2.
- Produces: `ValidatePlan(ctx, resolver, policy, plan) error`, `AllowedURL(ctx, resolver, policy, rawURL) error`, and deterministic worker redaction helpers.

- [ ] **Step 1: Write failing Go policy tests**

Use a fake resolver rather than the host resolver:

```go
type fakeResolver map[string][]net.IPAddr
func (r fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) { return r[host], nil }

func TestAllowedURLRejectsMetadataAndUnconfiguredPrivateIP(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.example.com"}}
	resolver := fakeResolver{
		"app.example.com": {{IP: net.ParseIP("169.254.169.254")}},
	}
	if err := AllowedURL(context.Background(), resolver, policy, "https://app.example.com/users"); !errors.Is(err, ErrBrowserDestinationBlocked) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidatePlanRejectsProdInteraction(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{IsProd: true, AllowedOrigins: []string{"https://app.example.com"}}
	plan := bughub.BrowserPlan{Version: 1, StartURL: "https://app.example.com", Actions: []bughub.BrowserAction{{ID: "click", Action: "click", Locator: &bughub.BrowserLocator{Kind: "text", Value: "提交"}}}}
	if err := ValidatePlan(context.Background(), fakeResolver{"app.example.com": {{IP: net.ParseIP("203.0.113.10")}}}, policy, plan); !errors.Is(err, ErrBrowserProdInteractionBlocked) {
		t.Fatalf("err = %v", err)
	}
}
```

Cover cross-origin redirects, `file:`/`data:`/`javascript:`, URL userinfo, IPv4-in-IPv6, loopback, RFC1918, unique-local IPv6, link-local, multicast, unspecified, metadata hostnames, and exact private-origin opt-in.

- [ ] **Step 2: Run the policy tests and verify they fail**

Run: `go test ./internal/browserverify -run 'TestAllowedURL|TestValidatePlan'`

Expected: FAIL because `internal/browserverify` and its sentinels do not exist.

- [ ] **Step 3: Implement the Go preflight policy**

```go
var (
	ErrBrowserDestinationBlocked = errors.New("browser destination is blocked")
	ErrBrowserOriginNotAllowed = errors.New("browser origin is not allowed")
	ErrBrowserProdInteractionBlocked = errors.New("browser interaction is blocked in production")
)

type IPResolver interface { LookupIPAddr(context.Context, string) ([]net.IPAddr, error) }

func ValidatePlan(ctx context.Context, resolver IPResolver, policy bughub.BrowserSecurityPolicy, plan bughub.BrowserPlan) error {
	if err := AllowedURL(ctx, resolver, policy, plan.StartURL); err != nil { return err }
	for _, action := range plan.Actions {
		if policy.IsProd && action.Action != "goto" && action.Action != "wait_for" && action.Action != "screenshot" {
			return fmt.Errorf("%w: action %s", ErrBrowserProdInteractionBlocked, action.ID)
		}
		if action.Action == "goto" {
			if err := AllowedURL(ctx, resolver, policy, action.URL); err != nil { return err }
		}
	}
	return nil
}
```

Normalize origins to lowercase scheme/host and explicit effective port. Resolve every hostname immediately before use. `PrivateOrigins` permits RFC1918/loopback only for that exact origin; it never permits link-local, metadata, unspecified, or multicast.

- [ ] **Step 4: Add offline worker sanitizer tests**

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { redactConsoleText, sanitizeURL, safeResponseRecord } from './sanitize.mjs';

test('sanitizes browser network and console evidence', () => {
  assert.equal(sanitizeURL('https://app.test/users?token=secret&q=%E6%B1%A4%E5%9C%86'), 'https://app.test/users?token=%5BREDACTED%5D&q=%E6%B1%A4%E5%9C%86');
  assert.equal(redactConsoleText('Authorization: Bearer top-secret password=hunter2'), '[REDACTED]');
  const record = safeResponseRecord({ method: 'GET', url: 'https://app.test/api?code=abc', status: 200, headers: { 'set-cookie': 'sid=secret', 'x-request-id': 'req-1' } });
  assert.equal(JSON.stringify(record).includes('secret'), false);
  assert.equal(record.request_id, 'req-1');
});
```

Create `sanitize.mjs` with no Playwright import. Export only those three pure functions. `sanitizeURL` parses with `new URL`, replaces values for case-insensitive keys matching `token|password|secret|code|session|auth|cookie|key`, removes URL userinfo, and returns the encoded URL. `redactConsoleText` returns `[REDACTED]` when the bounded 8 KiB message matches existing credential/header/bearer patterns. `safeResponseRecord` returns only `method`, sanitized `url`, integer `status`, `duration_ms`, `content_type`, `content_length`, `request_id`, and `trace_id`; it never copies arbitrary headers or bodies.

- [ ] **Step 5: Run Go and Node policy tests**

Run: `go test ./internal/browserverify && node --test internal/browserverify/worker/browser_worker.test.mjs`

Expected: PASS without importing or installing Playwright.

- [ ] **Step 6: Commit the policy boundary**

```bash
git add internal/browserverify/policy.go internal/browserverify/policy_test.go internal/browserverify/worker/sanitize.mjs internal/browserverify/worker/browser_worker.test.mjs
git commit -m "feat: enforce browser validation security policy"
```

---

### Task 4: Build the pinned host runtime and restricted Playwright worker

**Files:**
- Create: `internal/browserverify/runtime.go`
- Create: `internal/browserverify/runtime_test.go`
- Create: `internal/browserverify/verifier.go`
- Create: `internal/browserverify/verifier_test.go`
- Create: `internal/browserverify/worker/browser_worker.mjs`
- Create: `scripts/test-browser-worker.sh`

**Interfaces:**
- Consumes: Task 2 request/result types and Task 3 policy.
- Produces: `RuntimeManager`, `HostVerifier`, `Ensure`, `Repair`, `Status`, `Execute`, and a line-delimited JSON stdin/stdout worker protocol.

Use these runtime contracts:

```go
type RuntimeState string
const (
	RuntimeReady RuntimeState = "ready"
	RuntimeInstalling RuntimeState = "installing"
	RuntimeBroken RuntimeState = "broken"
)
type RuntimeStatus struct {
	State RuntimeState `json:"state"`
	Version string `json:"version"`
	ErrorCode string `json:"error_code,omitempty"`
	Message string `json:"message,omitempty"`
}
type RuntimePaths struct { Root, NodeModules, BrowsersPath, WorkerPath, Version string }
type CommandRunner interface {
	Run(context.Context, string, []string, []string, string, io.Reader, io.Writer, io.Writer) error
}
type RuntimeManager struct {
	managementRoot string
	runner CommandRunner
	mu sync.Mutex
	status RuntimeStatus
}
func NewRuntimeManager(managementRoot string, runner CommandRunner) *RuntimeManager
func (m *RuntimeManager) Ensure(context.Context, func(bughub.BrowserProgress)) (RuntimePaths, error)
func (m *RuntimeManager) Repair(context.Context, func(bughub.BrowserProgress)) (RuntimePaths, error)
func (m *RuntimeManager) Status() RuntimeStatus
```

`recordingCommandRunner` in the test stores executable/argument/environment/working-directory records, returns `FailContaining` when the rendered command contains that substring, and writes the fixed valid probe JSON to stdout for `--mode probe`.

Use this verifier construction boundary so tests never start Node:

```go
type WorkerRunner interface {
	Run(context.Context, RuntimePaths, workerRequest, func(bughub.BrowserProgress)) (workerResult, error)
}
type workerRequest struct {
	Mode string `json:"mode"`
	Plan bughub.BrowserPlan `json:"plan"`
	Policy bughub.BrowserSecurityPolicy `json:"policy"`
	StagingDir string `json:"staging_dir"`
	StorageStatePath string `json:"storage_state_path,omitempty"`
	Headless bool `json:"headless"`
}
type workerArtifact struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	RequestID string `json:"request_id,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
}
type workerResult struct {
	Status string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	FailedActionID string `json:"failed_action_id,omitempty"`
	FinalURL string `json:"final_url,omitempty"`
	Title string `json:"title,omitempty"`
	LoginOrigin string `json:"login_origin,omitempty"`
	FinalScreenshotPath string `json:"final_screenshot_path,omitempty"`
	AccessibilitySummary []bughub.BrowserAccessibilityNode `json:"accessibility_summary,omitempty"`
	Artifacts []workerArtifact `json:"artifacts"`
}
type HostVerifier struct {
	runtime *RuntimeManager
	worker WorkerRunner
	resolver IPResolver
}
func NewHostVerifier(runtime *RuntimeManager, worker WorkerRunner, resolver IPResolver) *HostVerifier
func (v *HostVerifier) Execute(context.Context, bughub.BrowserVerificationRequest) (bughub.BrowserVerificationResult, error)
```

`fakeWorker` returns its configured `workerResult`, increments `Calls`, and writes the declared artifact fixture bytes beneath the request staging directory before returning. `newTestHostVerifier` supplies a ready fake runtime and deterministic public-IP resolver; `validBrowserRequest` supplies a temp staging directory, test environment, public allowlisted origin, and one screenshot action; `completedWorkerResult` declares the fixed screenshot/network manifest.

- [ ] **Step 1: Write failing runtime lifecycle tests**

```go
func TestRuntimeManagerInstallsPinnedVersionAndRunsRealProbeCommand(t *testing.T) {
	runner := &recordingCommandRunner{}
	manager := NewRuntimeManager(t.TempDir(), runner)
	paths, err := manager.Ensure(context.Background(), nil)
	if err != nil { t.Fatal(err) }
	if paths.Version != "1.61.1" { t.Fatalf("version = %q", paths.Version) }
	got := strings.Join(runner.CommandSummaries(), "\n")
	for _, want := range []string{"npm install", "playwright install chromium", "browser_worker.mjs --mode probe"} {
		if !strings.Contains(got, want) { t.Fatalf("commands %q do not contain %q", got, want) }
	}
}

func TestRuntimeManagerDoesNotPublishFailedInstall(t *testing.T) {
	runner := &recordingCommandRunner{FailContaining: "playwright install chromium"}
	manager := NewRuntimeManager(t.TempDir(), runner)
	if _, err := manager.Ensure(context.Background(), nil); err == nil { t.Fatal("expected install failure") }
	if status := manager.Status(); status.State != RuntimeBroken || status.ErrorCode != "browser_runtime_install_failed" { t.Fatalf("status = %+v", status) }
	if _, err := os.Stat(manager.currentDir()); !errors.Is(err, os.ErrNotExist) { t.Fatalf("published failed runtime: %v", err) }
}
```

- [ ] **Step 2: Verify runtime tests fail**

Run: `go test ./internal/browserverify -run 'TestRuntimeManager'`

Expected: FAIL because `RuntimeManager` is undefined.

- [ ] **Step 3: Implement atomic runtime installation and probe**

Use this exact package manifest in the temporary version directory:

```json
{
  "name": "tshoot-browser-runtime",
  "private": true,
  "version": "1.61.1",
  "dependencies": { "playwright": "1.61.1" }
}
```

`Ensure` must:

1. Acquire an in-process mutex and an `O_CREATE|O_EXCL` install lock.
2. Create `<management-root>/browser-runtime/.install-1.61.1-<random>` with mode `0700`.
3. write `package.json`, embedded `browser_worker.mjs`, and embedded `sanitize.mjs` with `0600`.
4. run `npm install --ignore-scripts --no-audit --no-fund`, then the local `playwright install chromium` with `PLAYWRIGHT_BROWSERS_PATH=<management-root>/browser-runtime/1.61.1/browsers`.
5. run worker mode `probe`, which launches Chromium, opens a loopback data served by the worker, writes a PNG, and reports its SHA256.
6. fsync files/directories and rename the temporary directory to `<management-root>/browser-runtime/1.61.1`.
7. expose `ready`, `installing`, or `broken` with stable diagnostic codes; `Repair` removes only a verified broken version directory and repeats `Ensure`.

- [ ] **Step 4: Write failing fake-worker verifier tests**

```go
func TestHostVerifierReturnsOnlyManifestArtifacts(t *testing.T) {
	worker := &fakeWorker{Result: workerResult{
		Status: "completed",
		Artifacts: []workerArtifact{{Kind: "screenshot", Path: "browser/final.png"}, {Kind: "network", Path: "browser/network.json"}},
	}}
	verifier := newTestHostVerifier(t, worker)
	result, err := verifier.Execute(context.Background(), validBrowserRequest(t))
	if err != nil { t.Fatal(err) }
	if result.Status != "completed" || len(result.Artifacts) != 2 { t.Fatalf("result = %+v", result) }
	for _, artifact := range result.Artifacts {
		if filepath.IsAbs(artifact.Path) || strings.Contains(artifact.Path, "..") { t.Fatalf("unsafe artifact: %+v", artifact) }
	}
}

func TestHostVerifierReplaysCompletedManifestWithoutRerunningWorker(t *testing.T) {
	worker := &fakeWorker{Result: completedWorkerResult()}
	verifier := newTestHostVerifier(t, worker)
	req := validBrowserRequest(t)
	first, err := verifier.Execute(context.Background(), req)
	if err != nil { t.Fatal(err) }
	second, err := verifier.Execute(context.Background(), req)
	if err != nil { t.Fatal(err) }
	if worker.Calls != 1 || !reflect.DeepEqual(first, second) { t.Fatalf("calls=%d first=%+v second=%+v", worker.Calls, first, second) }
}
```

- [ ] **Step 5: Implement worker protocol, journal, and evidence manifest**

The Go verifier writes `browser/reservation.json` atomically before launch and `browser/result.json` atomically after validating every output file with the existing staging size/path rules. The reservation contains only attempt identity, plan SHA256, state, and rerun count. A matching completed result is replayed; an interrupted safe plan may be cleaned and rerun once; a second interrupted reservation returns `browser_execution_interrupted`.

The worker request is:

```json
{
  "mode": "execute",
  "plan": { "version": 1, "start_url": "https://app.test", "actions": [], "assertions": [] },
  "policy": { "allowed_origins": [], "private_origins": [], "auth_origins": [], "is_prod": false },
  "staging_dir": "/opaque-host-path/browser",
  "storage_state_path": "/temporary-decrypted-state.json"
}
```

The worker must:

- import only the Playwright-free `sanitize.mjs` at module load and dynamically import Playwright only inside `probe`, `execute`, or `login` mode;
- launch Chromium with a fresh context and optional storage state;
- revalidate scheme/origin/DNS/IP for navigation, redirects, and `context.route('**/*')` requests;
- implement only the seven declared actions with role/label/text/placeholder/test-id/css locators;
- emit progress records on stderr prefixed `TSHOOT_BROWSER_PROGRESS ` and one final JSON object on stdout;
- write PNGs, `network.json`, `console.jsonl`, and `browser-actions.json` under the supplied staging directory;
- omit screenshots whenever a visible password field or known login page is present;
- never record request/response bodies, Cookie, Authorization, raw headers, or traces;
- return `login_required`, `locator_failed`, `assertion_failed`, or `completed` with a bounded accessibility summary.

- [ ] **Step 6: Add offline and opt-in smoke entrypoints**

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
node --test internal/browserverify/worker/browser_worker.test.mjs
if [[ "${TSHOOT_BROWSER_SMOKE:-}" == "1" ]]; then
  go test ./internal/browserverify -run TestRealBrowserSmoke -count=1 -v
fi
```

The real smoke test starts a loopback HTTP server with a button, input, rendered result, local JSON API, console secret fixture, and auth headers; it asserts a non-empty final PNG plus sanitized network/console/action files.

- [ ] **Step 7: Run runtime/verifier offline tests**

Run: `go test ./internal/browserverify && scripts/test-browser-worker.sh`

Expected: PASS without a Playwright download. `TSHOOT_BROWSER_SMOKE=1 scripts/test-browser-worker.sh` is allowed to install/use the pinned local runtime and must pass separately before release.

- [ ] **Step 8: Commit the host runtime and worker**

```bash
git add internal/browserverify/runtime.go internal/browserverify/runtime_test.go internal/browserverify/verifier.go internal/browserverify/verifier_test.go internal/browserverify/worker/sanitize.mjs internal/browserverify/worker/browser_worker.mjs internal/browserverify/worker/browser_worker.test.mjs scripts/test-browser-worker.sh
git commit -m "feat: add Studio-hosted browser verifier runtime"
```

---

### Task 5: Encrypt and manage browser login sessions

**Files:**
- Create: `internal/browserverify/session.go`
- Create: `internal/browserverify/session_test.go`
- Modify: `internal/browserverify/verifier.go`
- Modify: `internal/browserverify/verifier_test.go`

**Interfaces:**
- Consumes: host verifier worker `login` and `execute` modes.
- Produces: `SessionStore.Load/Save/Clear`, `HostVerifier.Login`, `HostVerifier.ClearSession`, and a keyring-independent `SecretStore` interface.

- [ ] **Step 1: Write failing encryption and fallback tests**

```go
func TestSessionStorePersistsOnlyCiphertext(t *testing.T) {
	root := t.TempDir()
	secrets := newMemorySecretStore()
	store := NewSessionStore(root, secrets)
	key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test"}
	state := []byte(`{"cookies":[{"name":"sid","value":"plain-secret"}]}`)
	if err := store.Save(key, state); err != nil { t.Fatal(err) }
	data, err := os.ReadFile(store.encryptedPath(key))
	if err != nil { t.Fatal(err) }
	if bytes.Contains(data, []byte("plain-secret")) { t.Fatal("plaintext session persisted") }
	loaded, ok, err := store.Load(key)
	if err != nil || !ok || !bytes.Equal(loaded, state) { t.Fatalf("loaded=%q ok=%v err=%v", loaded, ok, err) }
}

func TestSessionStoreFallsBackToMemoryWhenKeyringUnavailable(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root, failingSecretStore{})
	key := SessionKey{SystemID: "base", Environment: "test", Origin: "https://app.test"}
	if err := store.Save(key, []byte(`{"cookies":[]}`)); err != nil { t.Fatal(err) }
	if _, err := os.Stat(store.encryptedPath(key)); !errors.Is(err, os.ErrNotExist) { t.Fatalf("session persisted without keyring: %v", err) }
	if _, ok, err := store.Load(key); err != nil || !ok { t.Fatalf("ok=%v err=%v", ok, err) }
}
```

Use these test stores in the same file:

```go
type memorySecretStore struct { values map[string]string }
func newMemorySecretStore() *memorySecretStore { return &memorySecretStore{values: map[string]string{}} }
func (s *memorySecretStore) Get(key string) (string, error) {
	value, ok := s.values[key]
	if !ok { return "", ErrSecretNotFound }
	return value, nil
}
func (s *memorySecretStore) Set(key, value string) error { s.values[key] = value; return nil }
func (s *memorySecretStore) Delete(key string) error { delete(s.values, key); return nil }
type failingSecretStore struct{}
func (failingSecretStore) Get(string) (string, error) { return "", errors.New("keyring unavailable") }
func (failingSecretStore) Set(string, string) error { return errors.New("keyring unavailable") }
func (failingSecretStore) Delete(string) error { return errors.New("keyring unavailable") }
```

- [ ] **Step 2: Verify session tests fail**

Run: `go test ./internal/browserverify -run 'TestSessionStore'`

Expected: FAIL because `SessionStore` does not exist.

- [ ] **Step 3: Implement AES-GCM storage and memory fallback**

```go
type SecretStore interface {
	Get(string) (string, error)
	Set(string, string) error
	Delete(string) error
}
var ErrSecretNotFound = errors.New("browser session key not found")
type SessionKey struct { SystemID, Environment, Origin string }
type SessionStore struct {
	root string
	secrets SecretStore
	mu sync.Mutex
	memory map[string][]byte
}

func (v *HostVerifier) SetSessionStore(store *SessionStore) { v.sessions = store }
func (v *HostVerifier) Repair(ctx context.Context, emit func(bughub.BrowserProgress)) error {
	_, err := v.runtime.Repair(ctx, emit)
	return err
}
func (v *HostVerifier) Status() RuntimeStatus { return v.runtime.Status() }
func (v *HostVerifier) ClearSession(_ context.Context, key SessionKey) error {
	if v.sessions == nil { return nil }
	return v.sessions.Clear(key)
}
```

Add `sessions *SessionStore` to `HostVerifier`; `Execute` loads the session keyed by system/environment/start origin, writes it to a `0600` plaintext temporary file only for the child process lifetime, and removes it on every return path.

Derive the keyring identifier as lowercase hex SHA256 of `systemID + "\x00" + environment + "\x00" + canonicalOrigin`. The desktop keyring adapter maps `keyring.ErrNotFound` to `ErrSecretNotFound`; only that sentinel means “generate a new key”, while any other Get/Set failure selects memory-only mode. Generate a random 32-byte AES key, store it base64-encoded in the keyring, and persist `{version:1, nonce:<base64>, ciphertext:<base64>}` with `0600`, temporary file, fsync, and rename. If the keyring is unavailable, put a cloned storageState only in the in-memory map and do not create a session file. `Clear` removes memory and ciphertext first, then treats both `ErrSecretNotFound` and a missing ciphertext file as success; an unavailable keyring is reported without reintroducing disk state.

- [ ] **Step 4: Add visible login mode without credential capture**

```go
type BrowserLoginRequest struct {
	SystemID string
	Environment string
	Origin string
	Policy bughub.BrowserSecurityPolicy
	Timeout time.Duration
	Emit func(bughub.BrowserProgress)
}
```

`HostVerifier.Login` decrypts an existing state into a `0600` temporary file if present, starts worker mode `login` with `headless:false`, waits until the page leaves a configured auth origin and no visible password field remains, reads the resulting storageState, encrypts it with `SessionStore.Save`, and securely removes the temporary plaintext. It never captures a login-page screenshot or console/network artifact.

- [ ] **Step 5: Run session and verifier tests**

Run: `go test ./internal/browserverify -run 'TestSessionStore|TestHostVerifierLogin|TestHostVerifierClearSession'`

Expected: PASS; scan the temp root and assert no file contains Cookie values, passwords, or the plaintext storageState.

- [ ] **Step 6: Commit session handling**

```bash
git add internal/browserverify/session.go internal/browserverify/session_test.go internal/browserverify/verifier.go internal/browserverify/verifier_test.go
git commit -m "feat: protect browser validation login sessions"
```

---

### Task 6: Coordinate planner, browser, one repair, evaluator, and durable evidence

**Files:**
- Create: `internal/bughub/workflow_browser_coordinator.go`
- Create: `internal/bughub/workflow_browser_coordinator_test.go`
- Modify: `internal/bughub/workflow_phase_runner.go`
- Modify: `internal/bughub/workflow_phase_runner_test.go`
- Modify: `internal/bughub/workflow_recovery.go`
- Modify: `internal/bughub/workflow_recovery_test.go`

**Interfaces:**
- Consumes: `BrowserVerifier`, `BrowserPolicyResolver`, validator execution bot, staging, and existing `ParsePhaseResult`/completion flow.
- Produces: browser-assisted phase execution with aggregated `AgentUsage`, stable browser failure codes, one repair maximum, and mandatory current-attempt screenshots.

Define the coordinator boundary exactly once in `workflow_browser_coordinator.go`:

```go
type BrowserCoordinator struct {
	Executor PhaseAgentExecutor
	Verifier BrowserVerifier
}
type BrowserCoordinatorRequest struct {
	Attempt PhaseAttempt
	Bug Bug
	Bot BotRef
	BasePrompt string
	Policy BrowserSecurityPolicy
	StagingDir string
	Emit func(InvestigationEvent)
}
type BrowserCoordinatorResult struct {
	FinalYAML string
	Usage AgentUsage
	BrowserArtifacts []BrowserArtifactReference
	BrowserResult BrowserVerificationResult
	RepairCount int
	ErrorCode string
	ErrorMessage string
}
func (c BrowserCoordinator) Execute(context.Context, BrowserCoordinatorRequest) (BrowserCoordinatorResult, error)
```

The test file defines `scriptedPhaseExecutor` as a FIFO slice of `PhaseExecutionResult`, `fakeBrowserVerifier` as a FIFO slice of `BrowserVerificationResult`, and YAML fixture functions returning fully literal BrowserPlan/ValidationResult documents. Both fakes increment `Calls`, fail when their slice is exhausted, and implement cancellation as a no-op.

- [ ] **Step 1: Write failing coordinator happy-path and repair tests**

```go
func TestBrowserCoordinatorPlansExecutesAndEvaluatesInOneAttempt(t *testing.T) {
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{
		{FinalYAML: validBrowserPlanYAML(), Usage: AgentUsage{InputTokens: 10, OutputTokens: 5}},
		{FinalYAML: reproducedValidationYAML("browser/final.png"), Usage: AgentUsage{InputTokens: 7, OutputTokens: 3}},
	}}
	verifier := &fakeBrowserVerifier{Result: completedBrowserResult("browser/final.png")}
	coordinator := BrowserCoordinator{Executor: executor, Verifier: verifier}
	result, err := coordinator.Execute(context.Background(), browserCoordinatorRequest(t))
	if err != nil { t.Fatal(err) }
	if executor.Calls != 2 || verifier.Calls != 1 { t.Fatalf("agent=%d browser=%d", executor.Calls, verifier.Calls) }
	if result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 8 { t.Fatalf("usage=%+v", result.Usage) }
	if len(result.BrowserArtifacts) == 0 || result.FinalYAML == "" { t.Fatalf("result=%+v", result) }
}

func TestBrowserCoordinatorRepairsLocatorOnlyOnce(t *testing.T) {
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{
		{FinalYAML: validBrowserPlanYAML()},
		{FinalYAML: repairedRemainingPlanYAML()},
		{FinalYAML: reproducedValidationYAML("browser/final.png")},
	}}
	verifier := &fakeBrowserVerifier{Results: []BrowserVerificationResult{
		{Status: "locator_failed", FailedActionID: "open-users", AccessibilitySummary: []BrowserAccessibilityNode{{Role: "tab", Name: "用户管理"}}},
		completedBrowserResult("browser/final.png"),
	}}
	result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), browserCoordinatorRequest(t))
	if err != nil { t.Fatal(err) }
	if executor.Calls != 3 || verifier.Calls != 2 || result.RepairCount != 1 { t.Fatalf("result=%+v", result) }
}
```

Add negative tests for login required, broken runtime, second locator failure, explicit Web request without URL, and an evaluator claiming success without a screenshot.

- [ ] **Step 2: Verify coordinator tests fail**

Run: `go test ./internal/bughub -run 'TestBrowserCoordinator'`

Expected: FAIL because `BrowserCoordinator` is undefined.

- [ ] **Step 3: Implement Web trigger and exact prompts**

```go
func browserAssistedAttempt(bug Bug, attempt PhaseAttempt) bool {
	if attempt.Phase != PhaseValidation && attempt.Phase != PhaseRegression { return false }
	if strings.TrimSpace(bug.FrontendURL) != "" { return true }
	userInput, _, _ := validationPromptInput(attempt.InputJSON)
	text := strings.ToLower(userInput)
	return strings.Contains(text, "web") || strings.Contains(text, "页面") || strings.Contains(text, "浏览器")
}
```

The planner prompt must include the Bug context, current continuation/regression scope, exact BrowserPlan schema, allowed actions, current environment, configured origins, production flag, and the instruction that it may output only BrowserPlan YAML. The evaluator prompt must contain only the sanitized execution report, safe accessibility summary, exact artifact relative paths, and the existing strict ValidationResult contract.

The repair prompt must contain completed action IDs, failed action ID, safe Playwright category, URL/title, and bounded accessibility nodes; require a plan containing only the failed and remaining actions. Reject any repaired plan that repeats a completed action or changes the start URL/origin.

- [ ] **Step 4: Implement coordinator outcome mapping**

Use these stable mappings:

```go
var browserOutcomeCodes = map[string]string{
	"login_required": "browser_login_required",
	"runtime_broken": "browser_runtime_broken",
	"locator_failed": "browser_locator_failed",
	"assertion_failed": "browser_assertion_failed",
	"policy_blocked": "browser_policy_blocked",
	"interrupted": "browser_execution_interrupted",
}
```

Login and a second locator/assertion failure become `PhaseOutcomeNeedsEvidence`; runtime/policy/validator failures remain system errors and must not be copied into user business `gaps`. Refactor validation decoding into `decodeValidationResultStrict` plus `validateValidationResult`. The coordinator strictly decodes evaluator YAML, injects/deduplicates host artifact references, and only then validates and applies the `ParsePhaseResult` outcome checks; host evidence therefore remains authoritative even if the evaluator omitted a path. Reject `reproduced`, `not_reproduced`, `fixed_verified`, or `still_reproduces` when the host result lacks a current-attempt `kind=screenshot` final PNG.

For a browser stop, persist only this bounded output envelope (with `login_origin` present only for login):

```go
mustJSON(map[string]any{
	"error_code": stableCode,
	"error_message": safePublicMessage,
	"failed_action_id": browserResult.FailedActionID,
	"login_origin": browserResult.LoginOrigin,
	"evidence_limitation": true,
})
```

Never put worker stderr, storageState, headers, accessibility HTML, or raw Playwright errors into `OutputJSON` or `ErrorMessage`.

- [ ] **Step 5: Inject coordinator dependencies into `AgentPhaseRunner`**

```go
type AgentPhaseRunner struct {
	// existing fields
	browserVerifier BrowserVerifier
	browserPolicyResolver BrowserPolicyResolver
}

func (r *AgentPhaseRunner) SetBrowserVerifier(verifier BrowserVerifier, resolver BrowserPolicyResolver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.browserVerifier = verifier
	r.browserPolicyResolver = resolver
}
```

In `run`, call the coordinator only when `browserAssistedAttempt` is true. Continue using the existing single Agent call for non-Web validation, investigation, and fix. Aggregate all Agent-call usage and wall duration into the attempt. Convert progress through this helper and pass it to `projectEvent`:

```go
func browserProgressEvent(progress BrowserProgress) InvestigationEvent {
	return InvestigationEvent{
		Type: "browser_progress",
		Message: progress.Message,
		Meta: map[string]any{
			"browser_code": progress.Code,
			"action_id": progress.ActionID,
			"current": progress.Current,
			"total": progress.Total,
		},
	}
}
```

- [ ] **Step 6: Register browser artifacts and enforce idempotent recovery**

Host artifact references use the same secure `staging.Capture` and `registerCapturedArtifact` path as Agent evidence. Before registering, confirm each host path appears in the signed result manifest and remains below 16 MiB. On recovery, a complete matching browser result manifest is replayed; a single interrupted safe reservation may be rerun; a second interruption completes with `browser_execution_interrupted`. Duplicate completion callbacks must find the existing `(attempt_id, sha256, kind)` artifact and never create a second row.

Adjust regression correlation validation without weakening environment/version freshness: every browser artifact must match target environment/version, while at least one artifact in the current attempt—not every screenshot—must carry a request ID or trace ID:

```go
hasCorrelation := false
for _, artifact := range result.ArtifactInputs {
	if artifact.Environment != input.TargetEnvironment { return errors.New("regression evidence environment does not match the target environment") }
	if artifact.Version != input.ObservedDeploymentVersion { return errors.New("regression evidence version does not match the observed deployment version") }
	hasCorrelation = hasCorrelation || strings.TrimSpace(artifact.RequestID) != "" || strings.TrimSpace(artifact.TraceID) != ""
}
if !hasCorrelation { return errors.New("regression evidence requires a fresh request_id or trace_id") }
```

- [ ] **Step 7: Run runner, orchestrator, and recovery tests**

Run: `go test ./internal/bughub -run 'TestBrowserCoordinator|TestAgentPhaseRunner.*Browser|TestRecover.*Browser|TestAgentPhaseRunner'`

Expected: PASS; the fake browser happy path reaches the existing reproduced/regression transition, while login and runtime failures remain distinct.

- [ ] **Step 8: Commit browser coordination**

```bash
git add internal/bughub/workflow_browser_coordinator.go internal/bughub/workflow_browser_coordinator_test.go internal/bughub/workflow_phase_runner.go internal/bughub/workflow_phase_runner_test.go internal/bughub/workflow_recovery.go internal/bughub/workflow_recovery_test.go
git commit -m "feat: coordinate durable rendered browser validation"
```

---

### Task 7: Wire desktop policy, login/runtime recovery, and safe artifact access

**Files:**
- Create: `cmd/tshoot-desktop/bindings_bug_browser.go`
- Create: `cmd/tshoot-desktop/bindings_bug_browser_test.go`
- Modify: `cmd/tshoot-desktop/main.go`
- Modify: `cmd/tshoot-desktop/bindings_bug_workflow.go`
- Modify: `cmd/tshoot-desktop/bindings_bug_workflow_test.go`
- Modify: `internal/bughub/workflow_artifacts.go`
- Modify: `internal/bughub/workflow_artifacts_test.go`
- Regenerate: `web/wailsjs/go/main/App.d.ts`
- Regenerate: `web/wailsjs/go/main/App.js`
- Regenerate: `web/wailsjs/go/models.ts`

**Interfaces:**
- Consumes: shared `HostVerifier`, installed system config, Case store, existing continuation command, and system keyring.
- Produces: `OpenIncidentBrowserLogin`, `RepairIncidentBrowserRuntime`, `ClearIncidentBrowserSession`, `GetIncidentArtifactPreview`, and `SaveIncidentArtifact` Wails bindings.

- [ ] **Step 1: Write failing artifact ownership and preview tests**

```go
func TestGetIncidentArtifactPreviewChecksCaseOwnershipAndPNGBytes(t *testing.T) {
	app, store := newBrowserBindingTestApp(t)
	artifact := registerPNGArtifact(t, store, "case-a", "attempt-a")
	preview, err := app.GetIncidentArtifactPreview("case-a", artifact.ID)
	if err != nil { t.Fatal(err) }
	if preview.MIMEType != "image/png" || preview.Base64Data == "" { t.Fatalf("preview=%+v", preview) }
	if _, err := app.GetIncidentArtifactPreview("case-b", artifact.ID); err == nil { t.Fatal("cross-case preview succeeded") }
}

func TestGetIncidentArtifactPreviewRejectsNonScreenshotAndChangedBytes(t *testing.T) {
	app, store := newBrowserBindingTestApp(t)
	artifact := registerTextArtifact(t, store, "case-a", "attempt-a")
	if _, err := app.GetIncidentArtifactPreview("case-a", artifact.ID); err == nil { t.Fatal("text preview succeeded") }
	if err := os.WriteFile(artifact.PathOrReference, []byte("changed"), 0o600); err != nil { t.Fatal(err) }
	if _, err := app.GetIncidentArtifactPreview("case-a", artifact.ID); err == nil { t.Fatal("changed artifact preview succeeded") }
}
```

Define the referenced helpers in `bindings_bug_browser_test.go` using the existing `newWorkflowBindingApp`: `newBrowserBindingTestApp` creates a Case plus current attempt and injects a `fakeIncidentBrowserController`; `registerPNGArtifact` writes the fixed 1×1 PNG byte fixture below `t.TempDir()` and calls `bughub.RegisterArtifact`; `registerTextArtifact` does the same with `[]byte("safe text")`. Both return the stored `EvidenceArtifact`, so no helper reaches into unexported artifact paths.

- [ ] **Step 2: Add a safe registered-artifact read API**

```go
type EvidenceArtifactContent struct {
	Artifact EvidenceArtifact
	Content []byte
}

func ReadEvidenceArtifact(ctx context.Context, store *CaseStore, caseID, artifactID string) (EvidenceArtifactContent, error) {
	artifacts, err := store.ListEvidenceArtifacts(ctx, caseID)
	if err != nil { return EvidenceArtifactContent{}, err }
	for _, artifact := range artifacts {
		if artifact.ID != artifactID { continue }
		captured, err := captureArtifactSource(artifact.PathOrReference)
		if err != nil { return EvidenceArtifactContent{}, err }
		if captured.SHA256 != artifact.SHA256 { return EvidenceArtifactContent{}, errors.New("registered artifact digest changed") }
		return EvidenceArtifactContent{Artifact: artifact, Content: captured.Content}, nil
	}
	return EvidenceArtifactContent{}, os.ErrNotExist
}
```

- [ ] **Step 3: Write failing policy/login/repair binding tests**

Cover all of these with a fake browser controller and fake config loader:

- environment Web/API/auth origins are canonicalized and passed to the runner;
- private origins are permitted only when present in the environment config;
- `OpenIncidentBrowserLogin` only accepts the current validation/regression attempt with `browser_login_required`;
- successful login calls `ContinueWithEvidence` once with the same Case/cycle and parent attempt;
- repeated login command uses the supplied idempotency key and does not create a second continuation;
- runtime repair continues only an attempt carrying `browser_runtime_broken`;
- clear-session is idempotent and does not mutate Case JSON;
- errors and emitted events contain no storageState, Cookie, Authorization, or password fixture.

- [ ] **Step 4: Own one browser controller in `App` and initialize it**

```go
type incidentBrowserController interface {
	bughub.BrowserVerifier
	Login(context.Context, browserverify.BrowserLoginRequest) error
	ClearSession(context.Context, browserverify.SessionKey) error
	Repair(context.Context, func(bughub.BrowserProgress)) error
	Status() browserverify.RuntimeStatus
}
```

Add `workflowBrowser incidentBrowserController` to `App`. During workflow initialization, pass `<workflowRoot>` as the runtime management root (the manager itself owns its `browser-runtime/1.61.1` child), build one session store rooted at `<workflowRoot>/browser-sessions`, and add a keyring adapter using service `tshoot-studio-browser-session`. Inject the controller and a `caseBrowserPolicyResolver` into `AgentPhaseRunner.SetBrowserVerifier`.

- [ ] **Step 5: Implement exact policy and recovery bindings**

Add these Wails input/result types:

```go
type IncidentBrowserCommandInput struct {
	CaseID string `json:"case_id"`
	AttemptID string `json:"attempt_id"`
	ExpectedVersion int64 `json:"expected_version"`
	IdempotencyKey string `json:"idempotency_key"`
	ActorID string `json:"actor_id"`
}
type IncidentArtifactPreview struct {
	ArtifactID string `json:"artifact_id"`
	MIMEType string `json:"mime_type"`
	Base64Data string `json:"base64_data"`
	Size int `json:"size"`
}
```

`OpenIncidentBrowserLogin` blocks in the Wails worker goroutine while the visible browser is open; after session save, it calls the same `ContinueWithEvidence` boundary used by `ContinueIncidentCase`. `RepairIncidentBrowserRuntime` repairs/probes first and then creates one continuation. `GetIncidentArtifactPreview` accepts only `kind=screenshot` plus PNG magic bytes and returns base64 without a filesystem path. `SaveIncidentArtifact` uses the Wails save dialog and writes only bytes returned by `ReadEvidenceArtifact`.

- [ ] **Step 6: Generate Wails bindings and run desktop tests**

Run:

```bash
go test ./internal/bughub ./cmd/tshoot-desktop -run 'Test.*Browser|TestGetIncidentArtifactPreview|TestReadEvidenceArtifact'
make wails-gen
go test ./cmd/tshoot-desktop
```

Expected: PASS and generated bindings expose all five browser/artifact methods.

- [ ] **Step 7: Commit desktop integration**

```bash
git add cmd/tshoot-desktop/main.go cmd/tshoot-desktop/bindings_bug_workflow.go cmd/tshoot-desktop/bindings_bug_workflow_test.go cmd/tshoot-desktop/bindings_bug_browser.go cmd/tshoot-desktop/bindings_bug_browser_test.go internal/bughub/workflow_artifacts.go internal/bughub/workflow_artifacts_test.go web/wailsjs/go/main/App.d.ts web/wailsjs/go/main/App.js web/wailsjs/go/models.ts
git commit -m "feat: expose secure incident browser controls"
```

---

### Task 8: Show browser progress, recovery actions, and rendered screenshot previews

**Files:**
- Create: `web/src/components/BugBrowserProgress.vue`
- Create: `web/src/components/BugBrowserProgress.test.ts`
- Modify: `web/src/lib/bridge/bugWorkflow.ts`
- Modify: `web/src/lib/bridge/bugWorkflow.test.ts`
- Modify: `web/src/lib/useIncidentCase.ts`
- Modify: `web/src/lib/useIncidentCase.test.ts`
- Modify: `web/src/components/BugCaseLifecycle.vue`
- Modify: `web/src/components/BugCaseLifecycle.test.ts`
- Modify: `web/src/components/BugCaseArtifacts.vue`
- Modify: `web/src/components/BugCaseArtifacts.test.ts`
- Modify: `web/src/pages/IncidentWorkbenchPage.vue`
- Modify: `web/src/pages/IncidentWorkbenchPage.test.ts`

**Interfaces:**
- Consumes: same-version `phase_event`, current attempt error code/output, and Wails browser/artifact bindings.
- Produces: live browser step list, login/repair/clear controls, and screenshot data-URL previews that never use artifact paths.

- [ ] **Step 1: Write failing same-version progress retention tests**

```ts
it('retains browser progress even when the Case snapshot version is unchanged', () => {
  const controller = createIncidentCaseController()
  controller.applySnapshot(detail({ version: 3 }))
  controller.acceptEvent({
    kind: 'snapshot', case: incident({ version: 3 }), snapshot: detail({ version: 3 }),
    phase_event: { type: 'browser_progress', message: '执行 2/4：切换用户页', meta: { case_id: 'case-1', attempt_id: 'attempt-1', browser_code: 'action_started', current: 2, total: 4 } },
  })
  expect(controller.phaseEvents.value['attempt-1']).toHaveLength(1)
  expect(controller.phaseEvents.value['attempt-1'][0].message).toContain('执行 2/4')
})
```

- [ ] **Step 2: Preserve bounded live phase events**

Add `phaseEvents = ref<Record<string, IncidentPhaseEvent[]>>({})` to the controller. In `acceptEvent`, append `phase_event` before version comparison, deduplicate by `(at,type,message,browser_code,action_id)`, retain the newest 100 per attempt, and clear entries when a newer attempt becomes current or its Case reaches a non-running status. Return `phaseEvents` from the controller.

- [ ] **Step 3: Add bridge methods and strict UI types**

```ts
export interface IncidentArtifactPreview { artifact_id: string; mime_type: 'image/png'; base64_data: string; size: number }
export interface IncidentBrowserCommandInput extends WorkflowCommandInput { attempt_id: string }

export async function openIncidentBrowserLogin(input: IncidentBrowserCommandInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.OpenIncidentBrowserLogin(input))
}
export async function repairIncidentBrowserRuntime(input: IncidentBrowserCommandInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.RepairIncidentBrowserRuntime(input))
}
export async function clearIncidentBrowserSession(caseID: string): Promise<void> {
  if (!isDesktop()) throw new Error(desktopOnly)
  await App.ClearIncidentBrowserSession(caseID)
}
export async function getIncidentArtifactPreview(caseID: string, artifactID: string): Promise<IncidentArtifactPreview> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return await App.GetIncidentArtifactPreview(caseID, artifactID) as IncidentArtifactPreview
}
export async function saveIncidentArtifact(caseID: string, artifactID: string): Promise<boolean> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return Boolean(await App.SaveIncidentArtifact(caseID, artifactID))
}
```

- [ ] **Step 4: Write and implement browser progress/recovery UI tests**

Tests must assert:

```ts
expect(wrapper.text()).toContain('准备验证浏览器')
expect(wrapper.text()).toContain('执行 2/4：切换到“用户”')
expect(wrapper.get('[data-browser-action="login"]').text()).toBe('打开验证浏览器完成登录')
expect(wrapper.get('[data-browser-action="clear-session"]').text()).toBe('清除此环境登录态')
expect(wrapper.get('[data-browser-action="repair-runtime"]').text()).toBe('修复浏览器环境并重试')
expect(wrapper.text()).not.toContain('Playwright 未安装，请提供 HAR')
```

`BugBrowserProgress` receives the current attempt and live events. It maps `browser_login_required`, `browser_runtime_broken`, `validator_not_installed`, locator failure, and business gaps to distinct copy/actions. Update `primaryActionFor` to inspect `IncidentCaseDetail`, so `waiting_evidence` with a browser system code does not show the generic evidence textarea.

- [ ] **Step 5: Write failing screenshot preview tests**

```ts
it('previews screenshots from safe bytes and never uses artifact paths as img src', async () => {
  vi.mocked(getIncidentArtifactPreview).mockResolvedValue({ artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'iVBORw0KGgo=', size: 8 })
  const wrapper = mount(BugCaseArtifacts, { props: { detail: screenshotDetail } })
  await flushPromises()
  const image = wrapper.get<HTMLImageElement>('img[data-artifact-id="shot-1"]')
  expect(image.attributes('src')).toBe('data:image/png;base64,iVBORw0KGgo=')
  expect(image.attributes('src')).not.toContain(screenshotDetail.artifacts[0].path_or_reference)
  expect(wrapper.text()).not.toContain(screenshotDetail.artifacts[0].path_or_reference)
})
```

- [ ] **Step 6: Implement screenshot cards and safe export**

For `kind=screenshot`, load preview bytes on mount/detail change, show a thumbnail, and open an accessible `<dialog>` with the full image. For network/console/action artifacts show type, capture time, environment, version, request/trace IDs, and “保存副本”, wired to `saveIncidentArtifact`; never render `path_or_reference` as user copy or DOM URL. Loading/preview errors remain local to the card and do not affect Case state.

- [ ] **Step 7: Dispatch actions in the workbench**

Add handlers using exact idempotency identities:

```ts
const browserKey = (kind: string, detail: IncidentCaseDetail) =>
  `${kind}:${detail.case.id}:${detail.case.current_attempt_id}:v${detail.case.version}`
```

Login and repair call their binding once through `incidentWorkflow.runOnce`, apply the returned Case snapshot, and refresh detail. Clear-session is idempotent and only refreshes UI after success. Continue preserving generic `supply_evidence` for actual business gaps.

- [ ] **Step 8: Run focused and full Web tests**

Run:

```bash
cd web
npx vitest run src/lib/bridge/bugWorkflow.test.ts src/lib/useIncidentCase.test.ts src/components/BugBrowserProgress.test.ts src/components/BugCaseLifecycle.test.ts src/components/BugCaseArtifacts.test.ts src/pages/IncidentWorkbenchPage.test.ts
npx vue-tsc --noEmit
npm run build
```

Expected: PASS; screenshot `<img>` sources are data URLs and browser progress updates without requiring a Case version change.

- [ ] **Step 9: Commit the UI**

```bash
git add web/src/lib/bridge/bugWorkflow.ts web/src/lib/bridge/bugWorkflow.test.ts web/src/lib/useIncidentCase.ts web/src/lib/useIncidentCase.test.ts web/src/components/BugBrowserProgress.vue web/src/components/BugBrowserProgress.test.ts web/src/components/BugCaseLifecycle.vue web/src/components/BugCaseLifecycle.test.ts web/src/components/BugCaseArtifacts.vue web/src/components/BugCaseArtifacts.test.ts web/src/pages/IncidentWorkbenchPage.vue web/src/pages/IncidentWorkbenchPage.test.ts
git commit -m "feat: present rendered browser validation evidence"
```

---

### Task 9: Align generated Agent guidance, ADR, documentation, and release verification

**Files:**
- Modify: `templates/workspace/skills/bug-verifier/SKILL.md.tmpl`
- Modify: `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`
- Modify: `scripts/test-skill-scripts.sh`
- Modify: `CONTRIBUTING.md`
- Modify: `docs/incident-workflow.md`
- Modify: `docs/troubleshooting-flow.md`
- Modify: `docs/decisions.md`

**Interfaces:**
- Consumes: completed browser runtime/coordinator/UI behavior.
- Produces: generated guidance that matches runtime behavior, append-only architecture decision, and the final verification record.

- [ ] **Step 1: Write the failing template contract test**

Add to the existing generator/investigation contract tests:

```go
func TestValidatorTemplateUsesStudioBrowserVerifierAsPrimary(t *testing.T) {
	content := renderSkillTemplate(t, "bug-verifier")
	for _, required := range []string{"Studio BrowserVerifier", "渲染截图", "browser_login_required", "不得直接启动 Playwright"} {
		if !strings.Contains(content, required) { t.Fatalf("missing %q", required) }
	}
	if strings.Contains(content, "后台运行时不保证有 in-app browser") { t.Fatal("obsolete browser downgrade guidance remains") }
}
```

- [ ] **Step 2: Update generated skills**

`bug-verifier` must state that Studio injects the BrowserPlan/execution/evidence protocol for Web attempts, host screenshots are authoritative, login is handled through the visible Studio browser, and the Agent must not start its own Playwright. `frontend-repro-investigator` must label `browser_collect.mjs` as a manual compatibility path only, not the durable Case path.

- [ ] **Step 3: Add offline worker tests to the standard script suite**

Append to `scripts/test-skill-scripts.sh`:

```bash
echo "▶ scripts/test-browser-worker.sh"
scripts/test-browser-worker.sh
```

Document the opt-in real command exactly as:

```bash
TSHOOT_BROWSER_SMOKE=1 scripts/test-browser-worker.sh
```

- [ ] **Step 4: Append the ADR and update workflow docs**

Append a new dated ADR to `docs/decisions.md` titled `验证浏览器由 Studio 宿主持有`. Record the accepted host-executor architecture, rejected workspace Playwright and external Browserless/Grid alternatives, pinned runtime/probe requirement, declarative action boundary, session encryption, Web screenshot success gate, and operational cost. Do not edit historical ADR entries.

Update the two workflow documents so validation/regression show:

```text
validator 规划 BrowserPlan
  → Studio 宿主执行并脱敏取证
  → 最多一次 locator 修正
  → validator 基于截图/Network/console 给出 ValidationResult
```

- [ ] **Step 5: Run the complete verification matrix**

Run:

```bash
go test ./internal/bughub ./internal/browserverify ./cmd/tshoot-desktop ./internal/config ./internal/generator
go test ./... -race
scripts/check-go-coverage.sh
scripts/test-skill-scripts.sh
cd web && npm test && npx vue-tsc --noEmit && npm run build
cd .. && go vet ./...
```

Expected: every command exits 0. Then run the opt-in local browser test once on a machine with Node/npm access:

```bash
TSHOOT_BROWSER_SMOKE=1 scripts/test-browser-worker.sh
```

Expected: Chromium launches a local page; final PNG is non-empty; network contains the local API but not Cookie/Authorization values; console contains the safe message but not the secret; action trace contains completed IDs.

- [ ] **Step 6: Inspect the final diff and sensitive-data fixtures**

Run:

```bash
git diff --check
git status --short
rg -n 'plain-secret|top-secret|hunter2|sid=secret' internal/browserverify cmd/tshoot-desktop web/src | head -100
```

Expected: `git diff --check` is clean; secret strings appear only in test fixtures/assertions; no runtime output, checked-in artifact, generated browser state, or `internal/webui/dist` build output is staged.

- [ ] **Step 7: Commit documentation and test integration**

```bash
git add templates/workspace/skills/bug-verifier/SKILL.md.tmpl templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl scripts/test-skill-scripts.sh CONTRIBUTING.md docs/incident-workflow.md docs/troubleshooting-flow.md docs/decisions.md
git commit -m "docs: define Studio-hosted browser validation workflow"
```

- [ ] **Step 8: Final commit audit**

Run:

```bash
git status --short
git log --oneline --decorate -10
```

Expected: only pre-existing unrelated user changes remain unstaged; the feature is represented by the nine focused commits above and no commit contains `internal/webui/dist/.gitkeep` deletion or unrelated files.
