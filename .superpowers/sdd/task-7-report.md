# Task 7 Report — analyzer topology pipeline and HEAD-aware cache

## Status

Implemented and verified on `feat/service-topology`.

Commit: `e08d30f feat: build topology during auto analysis`

## Files

- `internal/analyzerpipe/pipeline.go`
  - Added `Result.Topology` and topology schema initialization.
  - Builds deterministic service descriptors for every effective service-node repo from configured `ServiceNames`, falling back to `Repo.Name`; excludes common-lib/infra/docs.
  - Adds repo/service/role metadata, static service DNS aliases, and role-appropriate environment API/web hosts.
  - Runs endpoint scans after existing API/dependency/schema scans, preserves endpoints in existing analyzer repo findings, records per-repository scanned/failed/skipped state, and keeps partial results.
  - Runs deterministic matching, adapts config overrides to `topology.Override`, and merges automatic/candidate/hint/manual/stale evidence when at least two service repos produced endpoints.
- `internal/analyzerpipe/pipeline_test.go`
  - Added real local Node → PHP BFF → Go fixtures, service descriptor/host/alias assertions, override merge coverage, endpoint preservation, and missing-repo partial coverage.
- `internal/topology/types.go`
  - Added `topology.SchemaVersion = "1"`, used by both snapshots and cache invalidation.
- `internal/agent/auto_analyze.go`
  - Cache keys now include topology schema version and each sorted repo name/path plus `git -C <path> rev-parse HEAD`.
  - Uses deterministic `missing` and `not-git` sentinels.
- `internal/agent/auto_analyze_test.go`
  - Covers stable same-HEAD keys, changed-HEAD invalidation, schema version, sorted inputs, and unavailable-HEAD sentinels using real temporary Git repos.
- `internal/agent/auto_analyze_topology_test.go`
  - Proves four same-HEAD deployment calls perform one scan and three cache hits while reusing the same topology snapshot; a new commit triggers another scan.

## TDD evidence

### Pipeline RED

Command:

```text
go test ./internal/analyzerpipe/ -run TestRun_ServiceTopology -count=1
```

Observed expected compile failure:

```text
internal/analyzerpipe/pipeline_test.go:21:12: result.Topology undefined (type *Result has no field or method Topology)
...
FAIL github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe [build failed]
```

### Pipeline GREEN

```text
go test ./internal/analyzerpipe/ -run TestRun_ServiceTopology -count=1
ok github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe 0.525s
```

Package race verification also passed:

```text
go test ./internal/analyzerpipe/ -race -count=1
ok github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe 1.523s
```

### Cache RED

Command:

```text
go test ./internal/agent/ -run 'Topology|AutoAnalyzeCache' -count=1
```

Observed the three intended behavioral failures:

```text
TestAutoAnalyzeCacheKeyIncludesTopologySchemaAndRepositoryHeads: cache key lacks topology schema version
TestAutoAnalyzeCacheKeyUsesDeterministicUnavailableHeadSentinels: cache key lacks missing sentinel
TestRunAutoAnalyzeCacheReusesTopologyAcrossFourTargetsAndInvalidatesChangedHead: changed HEAD reused cached result
FAIL github.com/xiaolong/troubleshooter-studio/internal/agent
```

### Focused GREEN

Prescribed command:

```text
go test ./internal/analyzerpipe/ ./internal/agent/ -race -run 'Topology|AutoAnalyzeCache' -count=1
ok github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe 1.336s
ok github.com/xiaolong/troubleshooter-studio/internal/agent 1.948s
```

## Final verification

- `go test ./... -race` — PASS for every package. `cmd/tshoot-desktop.test` emitted the known non-fatal macOS `malformed LC_DYSYMTAB` linker warning.
- `go vet ./...` — PASS.
- `gofmt -l <all Task 7 Go files>` — no output.
- `git diff --check` — PASS.

## Self-review

- Confirmed `Snapshot.Services` is populated before matching for all effective service nodes, including repos that are missing locally; non-service roles never become graph nodes.
- Confirmed configured non-empty service names take precedence, with deterministic trim/dedup/sort and `Repo.Name` fallback.
- Confirmed endpoint scan failures are topology-local and do not discard existing dependency/API/schema findings or abort other repositories.
- Confirmed matcher `Edges` and low-confidence `Hints` both enter the evidence snapshot before override merge.
- Confirmed missing-repo partial analysis can retain an unmatched human confirmation as `stale` while preserving independently evidenced edges.
- Confirmed snapshot services/endpoints/repository states and aliases/hosts are deterministic.
- Confirmed cache probing uses real Git commands with no mutable command/global test seam, avoiding race-prone test overrides.
- Confirmed cache keys include system ID, topology contract version, sorted repo mapping, and HEAD/sentinel state; same HEAD reuses the exact cached `*analyzerpipe.Result`.
- Confirmed no MCP builder, schema, example, or unrelated generated file changed.
- Independent reviewer dispatch was intentionally not used because the supplied repository instruction forbids sub-agents unless explicitly requested; the review above was performed locally against the brief.

## Concerns

- Non-blocking environment warning only: the full race suite prints the repository's known macOS `LC_DYSYMTAB` linker warning but exits successfully.
- `topology.SchemaVersion` required a small change to `internal/topology/types.go`, which was not listed in the brief's file table but is necessary for the explicitly required `topology.SchemaVersion` cache contract.

---

## Fix Review — Task 7 review findings

### Status and commits

All Task 7 review findings were fixed on `feat/service-topology`.

Implementation commit: `be424ed fix: harden topology pipeline cache integration`

### RED evidence

Pipeline activation, service ownership, and alias regressions were added first:

```text
go test ./internal/analyzerpipe/ -run 'TestRun_ServiceTopology' -count=1
--- FAIL: TestRun_ServiceTopologyBuildsThreeRepositoryChain
    service "mall-web" metadata aliases included mall-web.default.svc / mall-web.svc instead of configured namespace DNS
--- FAIL: TestRun_ServiceTopologyActivationUsesRunnableLocalServiceRepositories/two_runnable_repos_merge_overrides_even_when_one_emits_no_endpoints
    missing edge caller -> empty path="/manual" in []topology.CandidateEdge(nil)
--- FAIL: TestRun_ServiceTopologyDuplicatesMultiServiceEndpointsWithoutBlankOwnership
    multi-service endpoints contained one Endpoint{Service:""}, want one copy per configured service
FAIL
```

The cache contract tests then failed at the wished-for config-aware API, before cache production changes:

```text
go test ./internal/agent/ -run 'TestAutoAnalyzeCacheKey' -count=1
internal/agent/auto_analyze_test.go:22:31: cannot use cfg (*config.SystemConfig) as string in argument to autoAnalyzeCacheKey
internal/agent/auto_analyze_test.go:23:14: undefined: autoAnalyzeCacheMaterialFor
FAIL github.com/xiaolong/troubleshooter-studio/internal/agent [build failed]
```

Explicit partial paths reproduced the sibling discovery bug:

```text
go test ./internal/agent/ -run TestRunAutoAnalyzeExplicitPartialPaths -count=1
repo "beta" summary=RepoSummary{Status:"analyzed" ...}, want status="skipped" error="not-found"
FAIL
```

After `ReposRoot` was cleared, the same test exposed the second source of fabrication: empty repository URLs shared `urlClonedTo[""]`. Strict explicit-path resolution was then applied at the pipeline boundary.

Concurrent same-key callers reproduced duplicate scans:

```text
go test ./internal/agent/ -run TestRunAutoAnalyzeConcurrentSameKeyCallersShareOneScan -count=1
12 concurrent callers started 12 scans, want 1
FAIL
```

### GREEN evidence

Focused pipeline verification:

```text
go test ./internal/analyzerpipe/ -run 'TestRun_ServiceTopology' -count=1
ok github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe 0.537s

go test ./internal/analyzerpipe/ -race -count=1
ok github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe 1.593s
```

Focused cache/path/concurrency verification:

```text
go test ./internal/agent/ -run 'TestAutoAnalyzeCacheKey' -count=1
ok github.com/xiaolong/troubleshooter-studio/internal/agent 0.892s

go test ./internal/agent/ -run TestRunAutoAnalyzeExplicitPartialPaths -count=1
ok github.com/xiaolong/troubleshooter-studio/internal/agent 0.467s

go test ./internal/agent/ -race -run 'ConcurrentSameKey|CanceledWaiter' -count=1
ok github.com/xiaolong/troubleshooter-studio/internal/agent 1.909s

go test ./internal/agent/ -race -run 'ConcurrentSameKey|CanceledWaiter' -count=10
ok github.com/xiaolong/troubleshooter-studio/internal/agent 5.182s
```

Affected-package and full-suite verification:

```text
go test ./internal/analyzerpipe/ ./internal/agent/ -race -run 'Topology|AutoAnalyzeCache|RunAutoAnalyze' -count=1
ok github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe 1.622s
ok github.com/xiaolong/troubleshooter-studio/internal/agent 2.629s

go test ./internal/analyzerpipe/ ./internal/agent/ -race -count=1
ok github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe 1.355s
ok github.com/xiaolong/troubleshooter-studio/internal/agent 4.529s

go test ./... -race
PASS: all repository packages

go vet ./...
# exit 0

gofmt -l <all changed Go files>
# no output

git diff --check
# exit 0
```

### Fix results

- Topology activation now counts successfully analyzed local service-repository entries, independently of endpoint count. Two runnable repositories always execute matcher/merge, so manual adds remain `manual` and unmatched confirmations remain `stale`; one runnable repository still skips graph matching and overrides.
- Endpoints with ambiguous ownership in a multi-service repository are duplicated once per configured service, assigned a non-empty service, and have IDs recomputed after assignment. Cross-repository-only matching behavior remains unchanged.
- Service aliases now contain the exact service name, plus repository name only for a single-service repository. K8s DNS aliases are emitted only from configured `k8s_runtime.service_map` namespaces as `service.namespace`, `service.namespace.svc`, and `service.namespace.svc.cluster.local`; frontend/mobile/admin and gateway environment host rules remain role-specific.
- The cache key is SHA-256 over canonical JSON containing system ID, SHA-256 of canonical full config JSON, topology schema, and sorted repository name/path/HEAD-sentinel records. The final key exposes neither config values nor secrets and cannot collide through path delimiters.
- `RunAutoAnalyze` passes an empty `ReposRoot`; pipeline strict explicit-path mode prevents omitted repositories from being discovered through parent, shared URL, or sibling/root inference.
- Concurrent cache misses coalesce through a per-key flight without holding the global mutex during scanning. Active waiters receive the same result/error pointer; caller cancellation is independent, and the shared scan is canceled only after every waiter cancels.

### Self-review

- Re-read all seven review findings against the final diff and confirmed each has a direct regression test.
- Confirmed runnable activation is per repository entry, not per service name or emitted endpoint.
- Confirmed multi-service expansion is deterministic because effective services are trimmed, deduplicated, and sorted before endpoint copies are produced.
- Confirmed no fabricated `.svc`, `.default.svc`, or namespace-free cluster-local alias remains, and a multi-service repository name is not shared across its services.
- Confirmed the full config digest covers service names, roles, API/web domains, topology overrides, repository-name aliases, and configured K8s namespace aliases.
- Confirmed cache material uses sorted structured records and canonical JSON rather than delimiter concatenation; the collision regression uses control characters in a path.
- Confirmed explicit-only path resolution still preserves normal `ReposRoot` behavior for callers that intentionally provide a repository root.
- Confirmed the cache/flight mutex protects only lookup, registration, reference count, and publication; endpoint/dependency/schema scans occur outside the mutex.
- Confirmed partial repository scan behavior, exact cached pointer reuse, HEAD invalidation, and the existing topology schema contract remain intact.
- Independent subagent review was not dispatched because repository/session instructions prohibit subagents unless explicitly requested; local review used the complete diff, requirement checklist, focused race tests, and full race suite.

### Concerns

- No blocking concerns.
- The full race suite continues to emit the repository's known non-fatal macOS `malformed LC_DYSYMTAB` linker warning and exits successfully.
