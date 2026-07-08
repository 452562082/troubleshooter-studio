# Single Bot Multi Agent Correction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep validation and troubleshooting as distinct internal agents with different skills, while exposing only one installed bot to Studio, Bug platform mapping, and bot selection.

**Architecture:** Generated IDE/OpenClaw artifacts may contain both a troubleshooter entry and a validator entry because external runtimes need separate execution prompts. Studio metadata, discovery, platform mapping, and UI treat them as one bot. Investigation derives the validator entry from the selected bot internally.

**Tech Stack:** Go discover/generator/agent/bughub/cmd bindings, Vue Bug Workbench/Bots UI, Vitest, Go tests.

---

### Task 1: Metadata And Discovery

**Files:**
- Modify: `internal/discover/types.go`
- Modify: `internal/discover/scan.go`
- Modify: `internal/generator/tshoot_meta.go`
- Modify: `internal/agent/install_native.go`
- Modify: `internal/agent/install_native_openclaw.go`

- [x] Remove role-specific discover semantics. `Scan` deduplicates by `system_id|target` and returns only root bot anchors.
- [x] Keep internal agent metadata inside the root `tshoot.json` as an `agents` array.
- [x] Do not write OpenClaw `agents-meta/*/tshoot.json` anchors.
- [x] IDE install may still use `agents-meta` internally to choose correct anchor, but installed discover anchors should represent the root bot.

### Task 2: Bug Investigation Routing

**Files:**
- Modify: `internal/bughub/types.go`
- Modify: `internal/bughub/match.go`
- Modify: `internal/bughub/codex_runner.go`
- Modify: `cmd/tshoot-desktop/bindings_bug_investigation.go`

- [x] Remove validator bot from public API input.
- [x] Keep `StartWithValidator` or equivalent internal helper, but derive validator entry from the selected bot.
- [x] For IDE targets, validator agent name is derived from selected bot metadata/system id.
- [x] For OpenClaw, validator agent id is derived from selected bot metadata/system id.

### Task 3: UI And Tests

**Files:**
- Modify: `web/src/pages/BugWorkbenchPage.vue`
- Modify: `web/src/components/BotCard.vue`
- Modify: `web/src/pages/BotsPage.vue`
- Modify tests under `internal/*` and `web/src/pages/BugWorkbenchPage.test.ts`

- [x] Remove validator filtering workarounds because validator is no longer returned by discover.
- [x] Remove visible role badges from bot cards.
- [x] Ensure platform config and bot picker show one bot.
- [x] Keep validation and investigation output tabs.
- [x] Run Go tests, Vitest, and web build.
