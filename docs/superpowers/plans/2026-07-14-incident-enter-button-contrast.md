# Incident Enter Button Contrast Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep the “进入故障闭环” primary action readable in default, hover, focus-visible, and disabled states.

**Architecture:** Preserve the existing button markup and global design system. Add action-panel-scoped state rules in `IncidentWorkbenchPage.vue` so the generic light hover rule cannot override a primary action, and lock the CSS contract with a source-level Vitest regression.

**Tech Stack:** Vue 3 single-file components, scoped CSS, Vitest, Vue Test Utils.

## Global Constraints

- Default state uses `#2563eb` with white text.
- Hover state uses `#1d4ed8` with white text and a 180ms color transition without layout movement.
- Focus-visible keeps the solid primary colors and a visible ring with 2px offset.
- Disabled state uses a neutral light-gray surface with dark-gray text and `not-allowed`, not a faded white-on-blue treatment.
- Do not change restart-button danger styling, layout, bot selection, Case state, or workflow behavior.
- Do not refactor the global `.btn` design system.
- Preserve the user's unrelated deletion of `internal/webui/dist/.gitkeep`; never stage it.

---

### Task 1: Make Bot action primary states readable

**Files:**
- Modify: `web/src/pages/IncidentWorkbenchPage.test.ts`
- Modify: `web/src/pages/IncidentWorkbenchPage.vue:828-852`

**Interfaces:**
- Consumes: existing `.bot-action-controls`, `.btn`, and `.btn.primary` selectors.
- Produces: an action-panel-scoped CSS state contract; no TypeScript or runtime API changes.

- [ ] **Step 1: Write the failing CSS regression test**

Add this test inside `describe('IncidentWorkbenchPage', ...)`:

```ts
it('keeps Bot action primary buttons readable across interaction states', () => {
  const source = readFileSync('src/pages/IncidentWorkbenchPage.vue', 'utf8')

  expect(source).toContain('.bot-action-controls .btn { flex: 1 1 160px; min-height: 44px; transition: background-color 180ms ease, border-color 180ms ease, color 180ms ease; }')
  expect(source).toContain('.bot-action-controls .btn.primary:hover:not(:disabled) { border-color: #1d4ed8; background: #1d4ed8; color: #fff; }')
  expect(source).toContain('.bot-action-controls .btn.primary:focus-visible { border-color: #2563eb; background: #2563eb; color: #fff; outline: 2px solid #1e40af; outline-offset: 2px; }')
  expect(source).toContain('.bot-action-controls .btn.primary:disabled { opacity: 1; border-color: #cbd5e1; background: #e2e8f0; color: #475569; cursor: not-allowed; }')
})
```

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
cd web
npx vitest run src/pages/IncidentWorkbenchPage.test.ts -t "keeps Bot action primary buttons readable across interaction states"
```

Expected: FAIL because the action-panel-specific transition, hover, focus-visible, and disabled rules do not exist.

- [ ] **Step 3: Add the minimal scoped CSS fix**

Replace the existing action-panel button rule and append the primary state rules:

```css
.bot-action-controls .btn { flex: 1 1 160px; min-height: 44px; transition: background-color 180ms ease, border-color 180ms ease, color 180ms ease; }
.bot-action-controls .btn.primary:hover:not(:disabled) { border-color: #1d4ed8; background: #1d4ed8; color: #fff; }
.bot-action-controls .btn.primary:focus-visible { border-color: #2563eb; background: #2563eb; color: #fff; outline: 2px solid #1e40af; outline-offset: 2px; }
.bot-action-controls .btn.primary:disabled { opacity: 1; border-color: #cbd5e1; background: #e2e8f0; color: #475569; cursor: not-allowed; }
```

The more specific hover selector must override `.btn:hover:not(:disabled)`, while focus and disabled states remain visually distinct.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run:

```bash
cd web
npx vitest run src/pages/IncidentWorkbenchPage.test.ts
```

Expected: all `IncidentWorkbenchPage` tests pass, including the new state-contract test.

- [ ] **Step 5: Run full Web verification**

Run each command from `web/`:

```bash
npm test -- --run
npx vue-tsc --noEmit
npm run build
```

Expected: all Web tests pass, type checking exits 0, and Vite reports a successful production build.

- [ ] **Step 6: Verify scope and commit**

Run:

```bash
git diff --check
git status --short
git diff -- web/src/pages/IncidentWorkbenchPage.vue web/src/pages/IncidentWorkbenchPage.test.ts
```

Expected: only the two planned Web files are changed in addition to the pre-existing, unrelated `.gitkeep` deletion.

Commit only the planned files:

```bash
git add web/src/pages/IncidentWorkbenchPage.vue web/src/pages/IncidentWorkbenchPage.test.ts
git commit -m "fix: improve incident enter button contrast"
```
