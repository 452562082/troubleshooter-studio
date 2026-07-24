# Bug Workbench Design

## Goal

Add a `Bug 工单` workspace page to Troubleshooter Studio so users can review bugs assigned to them from a bug platform, inspect reproduction context and attachments, choose an installed troubleshooting bot, and generate a structured troubleshooting context for that bot.

## Scope

First version supports:

- Sidebar entry: `Bug 工单`.
- Bug inbox with search, status filters, severity/environment/source labels.
- Bug detail view with title, source ID, assignee/reporter, environment, frontend URL, reproduction steps, attachments, trace/request IDs, screenshots/HAR/log hints.
- Manual bug import/paste as a fallback path.
- Zentao API pull for “assigned to me” bugs.
- Installed bot selection using existing `DiscoverBots` data.
- Structured troubleshooting context generation for the selected bot.

First version does not support:

- Webhook push receiver.
- Directly controlling external IDE conversations.
- Writing status changes back to Zentao.
- Multi-user server mode.

Webhook push can be added later after the local receiver, auth token, network reachability, and desktop background lifetime model are settled.

## User Flow

1. User opens `Bug 工单` from the sidebar.
2. Page loads local bug records and optionally pulls assigned bugs from Zentao API.
3. User selects a bug from the inbox.
4. Detail pane shows reproduction data, attachments, environment, and known IDs.
5. Right pane lists installed bots and recommends matches by system ID, repo names, env, frontend entry, or service hints.
6. User chooses a bot and clicks `生成排障上下文`.
7. Studio outputs a structured prompt/context package that can be copied into OpenClaw / Claude Code / Cursor / Codex, or saved into the bug record for later.

## Architecture

Keep bug-platform support as a Studio-side integration, not generated-bot behavior.

Frontend:

- `web/src/pages/BugWorkbenchPage.vue`
  - Owns inbox/detail/selected bot UI.
  - Calls bridge methods only; no platform-specific API logic in Vue.
- `web/src/lib/bridge/bugs.ts`
  - Desktop-first Wails bridge with browser fallback errors.
  - Methods: list bugs, import bug, refresh Zentao bugs, save config, generate context.
- `web/src/App.vue` and `web/src/router/index.ts`
  - Add sidebar entry and route.

Backend:

- `cmd/tshoot-desktop/bindings_bug.go`
  - Wails bindings for the page.
- `internal/bughub/`
  - Platform-neutral bug models and local store.
  - Zentao API client.
  - Context generator.
  - Bot matching helper.

Persistence:

- Local JSON store under `~/.tshoot/bugs/`.
- Integration config under `~/.tshoot/config.json` or a dedicated `~/.tshoot/bughub/config.json`.
- Secrets such as Zentao token/password should use existing keychain/credential patterns rather than plain UI-local storage.

## Data Model

Normalized bug record:

- `id`: local stable ID.
- `source`: `manual` or `zentao`.
- `source_id`: platform bug ID.
- `title`, `description`, `steps`, `expected`, `actual`.
- `status`, `severity`, `priority`.
- `assignee`, `reporter`, `created_at`, `updated_at`.
- `env`, `system_id`, `frontend_repo`, `service_hints`.
- `frontend_url`, `api_paths`, `trace_ids`, `request_ids`.
- `attachments`: name, type, local path or remote URL.
- `selected_bot_key`, `last_context`, `last_context_at`.

Bot match result:

- `bot_key`: derived from discovered bot path and target.
- `score`.
- `reasons`: matched system ID, env, repo, frontend entry, service hint.

## Zentao Integration

First version uses API pull:

- User configures Zentao base URL and token/session credentials.
- User clicks refresh or page refreshes manually.
- Client requests bugs assigned to configured account.
- Results are normalized and stored locally.

The client should be conservative:

- Network timeout.
- Clear auth errors.
- No destructive API calls.
- Keep raw source payload preview for debugging, but avoid storing secrets.

## Context Generation

Generated context should be deterministic text/markdown with:

- Bug title and source link/ID.
- Environment and frontend URL.
- Reproduction steps.
- Failed API paths and trace/request IDs.
- Attachment paths.
- Selected bot identity and installed target.
- Suggested first commands/actions for the target platform.

For now, the page copies this context to clipboard and shows it in a preview panel. Later versions may add “open in OpenClaw” or “send to IDE” once each platform’s invocation path is reliable.

## Error Handling

- Browser mode shows “desktop app required” for local bug store and Zentao credentials.
- Missing Zentao config shows an empty state with setup CTA.
- API failures keep existing local bugs visible and show a non-blocking error.
- Missing installed bots shows a CTA to `已装机器人` / `创建向导`.
- Missing attachments are flagged in the detail pane but do not block context generation.

## Testing

Backend:

- Unit tests for Zentao response normalization.
- Unit tests for local bug store read/write and idempotent upsert.
- Unit tests for bot match scoring.
- Unit tests for context generation.

Frontend:

- Route/sidebar tests if existing test harness covers App routing.
- Component tests for empty state, bug selection, bot selection, context preview.
- Bridge tests for desktop/browser behavior.

Manual:

- Import a pasted bug.
- Pull from a fake Zentao HTTP server.
- Select an installed bot and generate context.
