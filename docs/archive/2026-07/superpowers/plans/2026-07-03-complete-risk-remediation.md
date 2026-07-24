# Complete Risk Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Close the remaining project risks: Kuboard config-source runtime readability, stale Kubernetes wording, incomplete coverage/audit gates, and Node/Vite CI drift.

**Architecture:** Add a self-contained `kuboard_config.py` script under `config-executor` so generated robots can read Kuboard ConfigMap content without Wails bindings or kubectl. Update generated skill docs and UI copy to match the real capability, then broaden CI gates with focused thresholds and dependency audit checks.

**Tech Stack:** Go tests, Python 3 stdlib HTTP client, shell coverage gate, Vue/Vitest, GitHub Actions, GitLab CI.

---

### Task 1: Kuboard Config Executor Script

**Files:**
- Create: `templates/workspace/skills/config-executor/scripts/kuboard_config.py`
- Create: `templates/workspace/skills/config-executor/scripts/test_kuboard_config.py`
- Modify: `templates/workspace/skills/config-executor/SKILL.md.tmpl`

- [x] **Step 1: Write failing Python tests**

Create `test_kuboard_config.py` with two behaviors:
- `get` loads `~/.openclaw/<agent-id>-creds.json`, resolves cluster name to ID through Kuboard v4 `cluster-namespace-tree`, fetches one ConfigMap with `cluster-cache/direct`, and prints JSON containing `format: k8s-env-flat` plus raw `data`.
- Missing env credentials returns JSON `{error, hint}` and exit code `2`.

Run:

```bash
python3 templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
```

Expected before implementation: FAIL because `kuboard_config.py` does not exist.

- [x] **Step 2: Implement `kuboard_config.py`**

Use only stdlib modules: `argparse`, `json`, `os`, `ssl`, `sys`, `urllib`.

CLI contract:

```bash
python3 scripts/kuboard_config.py get \
  --agent-id <agent-id> --env <env> \
  --cluster <cluster-name> --namespace <namespace> --configmap <name>
```

Output on success:

```json
{
  "cluster": "dev-cluster",
  "namespace": "default",
  "configmap": "app-config",
  "format": "k8s-env-flat",
  "data": {"DB_HOST": "db", "REDIS_PORT": "6379"},
  "content": "{\"DB_HOST\":\"db\",\"REDIS_PORT\":\"6379\"}"
}
```

Support credentials from `~/.openclaw/<agent-id>-creds.json` and `~/.tshoot/<agent-id>-creds.json` in both shapes:

```json
{"kuboard":{"dev":{"url":"...","access_key":"..."}}}
{"kuboard":{"default":{"dev":{"url":"...","access_key":"..."}}}}
```

- [x] **Step 3: Wire config-executor docs**

Replace the current Kuboard section that says generated robots cannot read ConfigMaps. Document `kuboard_config.py get`, how routing supplies `cluster/namespace/configmap`, and that output should be piped into existing runtime parsing logic.

- [x] **Step 4: Verify tests**

Run:

```bash
python3 templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
go test ./internal/generator -run TestSkillScriptPathsExist
```

Expected: both PASS.

### Task 2: Remove Stale Kubernetes Wording

**Files:**
- Modify: `templates/workspace/skills/incident-investigator/SKILL.md.tmpl`
- Modify: `web/src/pages/InitPage.vue`
- Modify: `internal/config/types_observability.go`
- Modify: `.gitlab-ci.yml`
- Modify: `internal/doctor/fixer_test.go`

- [x] **Step 1: Confirm residue**

Run:

```bash
rg 'kubernetes ConfigMap|PrimaryConfigCenter.Type=env-vars` / `kubernetes|enabledSourceTypes.*kubernetes|nacos / apollo / consul / kubernetes|node:20 镜像|切到 20' templates web/src internal .gitlab-ci.yml -n
```

Expected before edit: matches in the files above.

- [x] **Step 2: Rewrite wording**

Use `kuboard/one2all` where the project means supported K8s-backed config sources. For K8s runtime comments, keep `kuboard` provider language and remove references to old `kubernetes` config-center type.

- [x] **Step 3: Verify residue removal**

Run the same `rg`; expected no matches.

### Task 3: Extend Coverage Gate

**Files:**
- Modify: `scripts/check-go-coverage.sh`
- Modify: `CONTRIBUTING.md`

- [x] **Step 1: Add thresholds for currently weak risk packages**

Add conservative gates:

```text
github.com/xiaolong/troubleshooter-studio/cmd/tshoot >= 0.7
github.com/xiaolong/troubleshooter-studio/internal/analyzer >= 29
github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe >= 17
github.com/xiaolong/troubleshooter-studio/internal/dsprobe >= 9
github.com/xiaolong/troubleshooter-studio/internal/userconfig >= 22
```

These start as non-regression floors, not aspirational quality targets.

- [x] **Step 2: Verify gate**

Run:

```bash
./scripts/check-go-coverage.sh
```

Expected: PASS with lines for all ten packages.

### Task 4: Add Dependency Audit Gates

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `.gitlab-ci.yml`
- Modify: `Makefile`
- Modify: `CONTRIBUTING.md`

- [x] **Step 1: Add frontend audit**

Add `npm audit --audit-level=moderate` after `npm ci --ignore-scripts` in GitHub and GitLab web jobs. Add a `make audit` target that runs the same check.

- [x] **Step 2: Add Go vulnerability check if tool is available**

Use a guarded local target:

```bash
if command -v govulncheck >/dev/null 2>&1; then govulncheck ./...; else echo "govulncheck not installed; skipping"; fi
```

Do not make CI install new tools in this change; keep CI deterministic.

- [x] **Step 3: Verify audit commands**

Run:

```bash
make audit
```

Expected: npm audit reports zero vulnerabilities; govulncheck either passes or explicitly skips.

### Task 5: Final Verification

**Files:**
- No additional code changes.

- [x] **Step 1: Full checks**

Run:

```bash
go test ./... -race
./scripts/check-go-coverage.sh
npm audit --audit-level=moderate
npm test -- --run
npm run build
git diff --check
```

Expected: all commands exit 0.

- [x] **Step 2: Residue scans**

Run:

```bash
rg 'resolve_runtime_k8s|PrimaryConfigCenter.Type "kubernetes"|config_center.*kubernetes|env-vars/kubernetes|nacos/apollo/consul/kubernetes' schema templates web/src internal docs examples -n -g '!docs/superpowers/**' -g '!internal/webui/dist/**'
rg 'npm install' README.md Makefile .github .gitlab-ci.yml cmd internal web -n -g '!web/node_modules/**' -g '!internal/webui/dist/**'
```

Expected: no matches.
