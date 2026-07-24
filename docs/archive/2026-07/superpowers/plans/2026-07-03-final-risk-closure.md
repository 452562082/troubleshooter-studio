# Final Risk Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the remaining risks after the complete remediation batch: Kuboard v3 generated-runtime support, CI-level Go vulnerability scanning, stale comments, and structured Kuboard runtime parsing.

**Architecture:** Extend the existing generated `kuboard_config.py` script instead of adding another runtime dependency. Add CI govulncheck as an explicit gate using Go's toolchain, and clean comment drift so docs, CI, and generated skills describe the same behavior.

**Tech Stack:** Python 3 stdlib, Go 1.25.11, govulncheck, GitHub Actions, GitLab CI, shell gates.

---

### Task 1: Kuboard v3 Runtime Script Support

**Files:**
- Modify: `templates/workspace/skills/config-executor/scripts/kuboard_config.py`
- Modify: `templates/workspace/skills/config-executor/scripts/test_kuboard_config.py`
- Modify: `templates/workspace/skills/config-executor/SKILL.md.tmpl`

- [x] **Step 1: Add failing v3 test**

Add a test where the mock Kuboard returns 404 for v4 `cluster-namespace-tree`, then serves v3 `GET /k8s-api/<cluster>/api/v1/namespaces/<namespace>/configmaps/<name>` using `Cookie: KuboardUsername=<user>; KuboardAccessKey=<access_key>`.

Run:

```bash
python3 templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
```

Expected before implementation: FAIL because `kuboard_config.py` cannot fall back to v3.

- [x] **Step 2: Implement v3 fallback**

Detect v3 when v4 tree returns 404 and `access_key` is present. Require username for v3. Fetch ConfigMap data from `/k8s-api/<cluster>/api/v1/namespaces/<namespace>/configmaps/<configmap>`.

- [x] **Step 3: Document v3/v4 support**

Update the Kuboard section in `config-executor` to state v4 token and v3 access-key+username are supported.

- [x] **Step 4: Add structured runtime summary**

Make `kuboard_config.py get` include a `runtime` object derived from flat ConfigMap keys such as `REDIS_HOST`, `MYSQL_HOST`, `MONGO_URI`, and `ELASTICSEARCH_URL`, so data-layer skills do not rely only on ad hoc LLM parsing.

### Task 2: CI Go Vulnerability Gate

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `.gitlab-ci.yml`
- Modify: `CONTRIBUTING.md`

- [x] **Step 1: Add govulncheck to CI**

GitHub Go job should install `golang.org/x/vuln/cmd/govulncheck@v1.5.0` and run `govulncheck ./...`.

GitLab Go test job should do the same before `go test`.

- [x] **Step 2: Verify locally**

Run:

```bash
make audit
```

Expected: npm audit reports zero vulnerabilities; govulncheck reports "Your code is affected by 0 vulnerabilities."

### Task 3: Stale Comment Cleanup

**Files:**
- Modify: `.gitlab-ci.yml`
- Modify: `web/src/lib/useImportCrossCheck.ts`

- [x] **Step 1: Remove stale version/type comments**

Replace stale `golang:1.25.10` with `golang:1.25.11`. Replace `env-vars / kubernetes / none` with `env-vars / none / unknown future source`.

- [x] **Step 2: Verify residue scan**

Run:

```bash
rg 'golang:1\\.25\\.10|env-vars / kubernetes / none|node:20 镜像|go 1\\.25\\.10|golang:1\\.25\\.10' .gitlab-ci.yml web/src -n
```

Expected: no matches.

### Task 4: Final Verification

**Files:**
- No additional code changes.

- [x] **Step 1: Run focused checks**

```bash
python3 templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
go test ./internal/generator -run TestSkillScriptPathsExist
make audit
```

- [x] **Step 2: Run full checks**

```bash
go test ./... -race
./scripts/check-go-coverage.sh
cd web && npm test -- --run
cd web && npm run build
git diff --check
```

Expected: all commands exit 0.
