# Codex Bug Investigation Design

## Goal

Turn the Bug 工单 page from a context-copy workflow into a direct investigation launcher for Codex-installed troubleshooting bots.

The first version supports only `codex` target bots. A user selects a Bug, selects a Codex bot, clicks `开始排障`, and Studio runs Codex in the background with the generated troubleshooting context. Studio streams progress and the final result back into the Bug 工单 page.

## Non-Goals

- Do not automate clicks or text entry inside the Codex UI.
- Do not directly control OpenClaw, Cursor, or Claude Code in this version.
- Do not write results back to Zentao.
- Do not allow unrestricted filesystem access by default.
- Do not modify generated bot workspaces.

Non-Codex bots remain selectable for later phases, but their primary action is a disabled or fallback state explaining that direct launch currently requires a Codex target.

## User Flow

1. User opens `Bug 工单`.
2. User selects a synced Bug.
3. User selects an installed bot whose target is `codex`.
4. Right pane shows `开始排障` instead of only `生成上下文`.
5. User clicks `开始排障`.
6. Studio starts a background Codex run and switches the right pane into a run view.
7. Run view shows status, streamed events, and final answer.
8. User can stop a running investigation or copy the final result.

If Codex CLI is missing, unauthenticated, or the bot cannot be resolved, Studio shows a blocking error with the exact failed prerequisite. If the selected bot is not `codex`, Studio shows a direct-launch unsupported state and keeps copy-context as a fallback.

## Architecture

### Frontend

`web/src/pages/BugWorkbenchPage.vue` owns the interaction:

- Replace `生成上下文` as the primary Codex action with `开始排障`.
- Keep `复制上下文` as a secondary fallback.
- Show active run state: queued/running/succeeded/failed/cancelled.
- Stream readable run events into the preview panel.
- Disable duplicate starts for the same Bug while a run is active.

`web/src/lib/bridge/bugs.ts` adds desktop bridge calls:

- `startBugInvestigation(input)`
- `cancelBugInvestigation(input)`
- `listBugInvestigationRuns(bugID)`

Browser mode throws clear desktop-only errors for start/cancel.

### Backend

`cmd/tshoot-desktop/bindings_bug.go` exposes Wails bindings that delegate to `internal/bughub`.

`internal/bughub` adds:

- Run model and store under `~/.tshoot/bugs/runs.json`.
- Codex command builder.
- Process manager for active investigations.
- JSONL event parser for `codex exec --json`.
- Prompt builder that reuses the existing deterministic Bug context.

The command shape is:

```bash
codex exec --json --cd <bot-or-workspace-dir> <prompt>
```

The prompt must explicitly ask Codex to use the selected troubleshooting bot behavior and investigate the selected Bug end-to-end. It should include the normalized Bug context, selected bot metadata, and constraints:

- focus on root-cause investigation,
- use read-only diagnostics unless the user later approves writes,
- summarize findings and next actions,
- do not change code unless the prompt explicitly allows it.

## Workspace And Safety

Codex runs with conservative defaults:

- `--sandbox workspace-write` only when a valid workspace directory exists.
- `--sandbox read-only` if Studio cannot safely identify a writable workspace.
- Approval mode defaults to `on-request` for interactive runs where possible.

The first implementation should not use `--dangerously-bypass-approvals-and-sandbox`.

The working directory should prefer the bot path if it is a real Codex bot workspace. If that is not a Git repository, fall back to the current Studio workspace only when explicitly safe. If neither is safe, fail before starting Codex.

## Run Persistence

Each investigation run stores:

- `id`
- `bug_id`
- `bot_key`
- `status`
- `started_at`
- `finished_at`
- `prompt_preview`
- `events`
- `final_message`
- `error`

Events are append-only while the run is active. The backend also emits Wails events for newly appended run events so the page can update without waiting for a full refresh. If the event bridge is unavailable, the page can reload the run list as a fallback without changing backend behavior.

## Error Handling

- Missing Codex CLI: show `未检测到 codex CLI`.
- Codex exits non-zero: mark run failed and show stderr or JSONL `error`.
- User cancels: terminate the process, mark run cancelled.
- Existing active run for the Bug: reuse/show that run instead of starting a duplicate.
- Malformed JSONL event: keep the raw line as a log event instead of failing the whole run.

## Testing

Backend:

- Unit test command construction for Codex runs.
- Unit test run store create/update/list behavior.
- Unit test JSONL event parsing for `thread.started`, `item.*`, `turn.completed`, `turn.failed`, and malformed lines.
- Binding test with a fake `codex` executable that emits JSONL and exits successfully.
- Binding test for cancel path.

Frontend:

- Bridge tests for start/cancel/list desktop calls.
- Component test that Codex bot shows `开始排障`.
- Component test that non-Codex bot shows unsupported direct-launch state.
- Component test that a running investigation disables duplicate start and renders final output.

Manual:

- Select a real Codex bot from Bug 工单.
- Start investigation on a synced Zentao Bug.
- Confirm progress appears and final answer is copyable.
- Confirm cancel works on a long-running fake run.

## Deferred Decisions

- User-selectable sandbox mode is deferred. First version keeps conservative backend defaults and does not expose a sandbox selector.
- `codex app-server` integration is deferred. First version uses `codex exec --json` because it is documented for non-interactive automation and easier to test.
