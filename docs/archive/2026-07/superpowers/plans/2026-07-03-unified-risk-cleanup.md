# Unified Risk Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the remaining project risks identified after the first cleanup: unenforced coverage gates, unsupported Kubernetes config-center residue, partial `tshoot serve` ambiguity, npm install drift, frontend dev dependency audit findings, and missing serve wiring tests.

**Architecture:** Keep fixes small and guard-heavy. Add one local coverage checker script and wire it into CI/Makefile, remove obsolete `kubernetes` wording from generated/runtime-facing surfaces, keep `tshoot serve` as a documented lightweight browser UI, and test the HTTP server wiring through `httptest`.

**Tech Stack:** Go 1.25, shell scripts, net/http/httptest, Vue/Vitest, existing CI YAML.

---

### Task 1: Coverage Gate

**Files:**
- Create: `scripts/check-go-coverage.sh`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`
- Modify: `.gitlab-ci.yml`
- Modify: `CONTRIBUTING.md`

- [x] **Step 1: Write a failing executable script test by running the missing script**

Run:

```bash
scripts/check-go-coverage.sh
```

Expected: FAIL with “no such file or directory”.

- [x] **Step 2: Add script**

Create `scripts/check-go-coverage.sh` that runs `go test -cover ./...`, captures output, and fails if these packages drop below documented thresholds:

```text
github.com/xiaolong/troubleshooter-studio/api >= 50
github.com/xiaolong/troubleshooter-studio/internal/agent >= 60
github.com/xiaolong/troubleshooter-studio/internal/generator >= 60
github.com/xiaolong/troubleshooter-studio/internal/deploy >= 80
github.com/xiaolong/troubleshooter-studio/internal/doctor >= 70
```

- [x] **Step 3: Run focused verification**

Run:

```bash
scripts/check-go-coverage.sh
```

Expected: PASS and print one line per checked package.

- [x] **Step 4: Wire gate into build**

Update `Makefile test`, GitHub CI, GitLab CI, and CONTRIBUTING to use the script instead of merely printing coverage.

### Task 2: Remove Unsupported `kubernetes` Config-Center Residue

**Files:**
- Modify: `schema/troubleshooter.schema.yaml`
- Modify: `templates/workspace/CHECKLIST.md.tmpl`
- Modify: `templates/workspace/skills/config-executor/SKILL.md.tmpl`
- Modify: `templates/workspace/skills/*-runtime-query/SKILL.md.tmpl`
- Modify: `web/src/lib/yamlGenerator.ts`
- Modify comments in `web/src/pages/InitPage.vue` and tests/docs that still imply `kubernetes` is a supported config-center type

- [x] **Step 1: Confirm current residue**

Run:

```bash
rg 'config_center.*kubernetes|PrimaryConfigCenter.Type "kubernetes"|type: kubernetes|env-vars/kubernetes|nacos/apollo/consul/kubernetes' schema templates web/src internal docs examples
```

Expected: find unsupported config-center residue.

- [x] **Step 2: Remove or rewrite residue**

Replace supported-type comments with `kuboard` / `one2all`. Remove template branches that only render for impossible `PrimaryConfigCenter.Type == "kubernetes"`.

- [x] **Step 3: Verify residue removal**

Run the same `rg` command.

Expected: no generated/runtime-facing unsupported config-center residue remains. Generic Kubernetes log-label/code-scanning references may remain.

### Task 3: `tshoot serve` Scope and Wiring

**Files:**
- Modify: `cmd/tshoot/serve.go`
- Modify: `cmd/tshoot/serve_test.go`
- Modify: `README.md`
- Modify: `web/src/pages/AnalyzePage.vue`

- [x] **Step 1: Add failing test for HTTP server wiring**

Add a test using `httptest.NewServer(newServeHandler(...))` that GETs `/api/schema` and expects `200` plus `schema_version`.

- [x] **Step 2: Run focused test**

Run:

```bash
go test ./cmd/tshoot -run TestServeHandler
```

Expected: FAIL because `newServeHandler` does not exist.

- [x] **Step 3: Extract handler helper**

Add `newServeHandler(templateRoot string) http.Handler` and have `runServe` call it.

- [x] **Step 4: Clarify browser limitations**

Update README and browser error text to say `tshoot serve` supports validate/plan/gen/doctor/schema and static UI, while repo analyze/install/native filesystem operations remain desktop/CLI.

### Task 4: NPM Install Consistency

**Files:**
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`
- Modify: `internal/webui/placeholder/index.html`
- Modify: `cmd/tshoot-desktop/wails.json`
- Modify: `CONTRIBUTING.md` if needed

- [x] **Step 1: Update install commands**

Use `npm ci --ignore-scripts` for CI and Makefile web builds. Keep `npm run build` unchanged. Upgrade the Vite dev-server toolchain if audit flags vulnerable dev-server dependencies, and pin the Node floor required by that toolchain.

- [x] **Step 2: Verify web build**

Run:

```bash
npm ci --ignore-scripts
npm audit --audit-level=moderate
npm test -- --run
npm run build
```

Expected: all PASS.

### Task 5: Final Verification

**Files:**
- No additional file changes expected.

- [x] **Step 1: Run final commands**

Run:

```bash
go test ./... -race
scripts/check-go-coverage.sh
npm test -- --run
npm run build
git diff --check
```

Expected: all PASS.
