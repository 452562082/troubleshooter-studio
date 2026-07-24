# Attachment Preview Close Button Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the visually heavy text-based attachment preview close control with an accessible 44×44 circular ghost button containing a stable SVG icon.

**Architecture:** Keep the change local to `BugInboxPage.vue`: the attachment preview gets a dedicated button class and inline SVG, while every other `.btn.icon` control remains unchanged. Extend the existing page test to specify semantic markup and closing behavior before changing production code.

**Tech Stack:** Vue 3 Single File Components, TypeScript, scoped CSS, Vitest, Vue Test Utils

## Global Constraints

- Only the Bug ticket attachment preview close button changes; platform configuration icon buttons remain unchanged.
- The interactive target is exactly 44×44 pixels and does not shrink inside the modal header.
- The visible close mark is a 20×20 inline SVG with `aria-hidden="true"`; the button retains `aria-label="关闭附件预览"`.
- Hover, active, and keyboard focus states must not cause layout movement.
- Existing overlay-click closing, modal sizing, and attachment rendering remain unchanged.

---

### Task 1: Replace the attachment preview close control

**Files:**
- Modify: `web/src/pages/BugInboxPage.test.ts`
- Modify: `web/src/pages/BugInboxPage.vue`
- Reference: `docs/superpowers/specs/2026-07-13-attachment-preview-close-button-design.md`

**Interfaces:**
- Consumes: `attachmentPreview: Ref<BugAttachmentPreviewResult | null>` and its existing `null` close state.
- Produces: `button.attachment-preview-close[aria-label="关闭附件预览"]` with an inline decorative SVG; clicking it assigns `attachmentPreview = null`.

- [ ] **Step 1: Extend the existing attachment preview test with the required markup and close behavior**

In `web/src/pages/BugInboxPage.test.ts`, append these assertions to `it('previews attachments from the shared full detail', ...)` after the existing image `src` assertion:

```ts
    const closeButton = wrapper.get('button.attachment-preview-close[aria-label="关闭附件预览"]')
    expect(closeButton.find('svg[aria-hidden="true"]').exists()).toBe(true)
    expect(closeButton.text()).toBe('')

    await closeButton.trigger('click')

    expect(wrapper.find('.attachment-preview-modal').exists()).toBe(false)
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
npm --prefix web test -- --run src/pages/BugInboxPage.test.ts
```

Expected: FAIL because `button.attachment-preview-close[aria-label="关闭附件预览"]` does not exist in the current text-character implementation.

- [ ] **Step 3: Replace the text character with the dedicated SVG button**

In the attachment preview header in `web/src/pages/BugInboxPage.vue`, replace the current one-line close button with:

```vue
        <header>
          <strong>{{ attachmentPreview.name }}</strong>
          <button class="attachment-preview-close" type="button" aria-label="关闭附件预览" @click="attachmentPreview = null">
            <svg aria-hidden="true" viewBox="0 0 24 24" fill="none">
              <path d="M6 6l12 12M18 6 6 18" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
            </svg>
          </button>
        </header>
```

- [ ] **Step 4: Add isolated visual and interaction styles**

In the attachment preview CSS block in `web/src/pages/BugInboxPage.vue`, immediately after `.attachment-preview-modal header strong`, add:

```css
.attachment-preview-close {
  flex: 0 0 44px;
  width: 44px;
  height: 44px;
  padding: 0;
  display: inline-grid;
  place-items: center;
  border: 0;
  border-radius: 999px;
  background: transparent;
  color: var(--c-muted);
  cursor: pointer;
  transition: background-color 160ms ease, color 160ms ease;
}
.attachment-preview-close svg { width: 20px; height: 20px; }
.attachment-preview-close:hover { background: var(--c-surf-2); color: var(--c-text); }
.attachment-preview-close:active { background: var(--c-line); }
.attachment-preview-close:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
@media (prefers-reduced-motion: reduce) {
  .attachment-preview-close { transition: none; }
}
```

- [ ] **Step 5: Run the focused test and verify GREEN**

Run:

```bash
npm --prefix web test -- --run src/pages/BugInboxPage.test.ts
```

Expected: PASS for every `BugInboxPage.test.ts` case, including SVG markup and click-to-close behavior.

- [ ] **Step 6: Run repository-level Web verification**

Run:

```bash
npm --prefix web test -- --run
npm --prefix web run build
make lint
git diff --check
```

Expected: all Web tests pass, the production build completes, lint reports `go vet + gofmt clean`, TypeScript emits no errors, and `git diff --check` prints nothing. If the Web build removes `internal/webui/dist/.gitkeep`, restore its existing tracked content before committing and do not stage generated `web/dist` output.

- [ ] **Step 7: Commit the tested implementation**

Run:

```bash
git add web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts
git diff --cached --check
git commit -m "fix: refine attachment preview close button"
```

Expected: one implementation commit containing only the Vue component and its test.
