# Zentao Personal Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a no-admin Bug intake path that syncs "assigned to me" Zentao bugs with a manual trigger path for immediate triage.

**Architecture:** Keep Bug storage in `internal/bughub`. Add a Zentao sync service that authenticates with either a saved token or personal account/password, fetches assigned bugs or a specific Bug ID, and upserts them into the existing local Bug store. Desktop bindings expose sync operations to Vue, and a desktop background poller periodically syncs enabled Zentao platforms.

**Tech Stack:** Go, Wails desktop bindings, Vue 3, Vitest, existing `internal/bughub` JSON store.

---

### Task 1: Backend Zentao Sync

**Files:**
- Modify: `internal/bughub/zentao.go`
- Create: `internal/bughub/sync.go`
- Modify: `internal/bughub/zentao_test.go`

- [ ] Add failing tests for fetching a single Zentao bug and syncing assigned bugs into a local store.
- [ ] Implement `ZentaoClient.FetchByID(id string)` using `/api.php/v1/bugs/{id}` and existing token headers.
- [ ] Implement account/password token exchange through `/api.php/v1/tokens` when no saved token is configured.
- [ ] Implement `SyncZentaoAssigned(platform PlatformConfig, store *Store)` and `SyncZentaoBug(platform PlatformConfig, store *Store, bugID string)`.
- [ ] Return a small `SyncResult` with fetched/stored counts and selected bug ID.

### Task 2: Desktop Runtime

**Files:**
- Modify: `cmd/tshoot-desktop/bindings_bug.go`
- Modify: `cmd/tshoot-desktop/main.go`
- Modify: `cmd/tshoot-desktop/main_test.go`

- [ ] Add Wails bindings `SyncBugPlatform(platformID string)` and `FetchBugByID(input BugFetchInput)`.
- [ ] Start a background poller on desktop startup that syncs enabled Zentao platforms every minute.
- [ ] Keep failures non-fatal and log them to stdout so the tray/background app remains alive.

### Task 3: Frontend Workbench

**Files:**
- Modify: `web/src/lib/bridge/bugs.ts`
- Modify: `web/src/lib/bridge/bugs.test.ts`
- Modify: `web/src/pages/BugWorkbenchPage.vue`
- Regenerate: `web/wailsjs/go/main/App.js`, `web/wailsjs/go/main/App.d.ts`, `web/wailsjs/go/models.ts`

- [ ] Add bridge calls for sync and fetch-by-ID.
- [ ] Add UI actions: "立即同步" and "按 Bug ID 拉取".
- [ ] Rename inbox copy from Hook-only wording to personal sync plus Hook wording.
- [ ] Regenerate Wails bindings after adding Go exported methods.

### Task 4: Verification

**Files:** no production edits expected.

- [ ] Run `go test ./... -count=1`.
- [ ] Run `cd web && npm test`.
- [ ] Run `cd web && npm run build`.
- [ ] Run `git diff --check`.
