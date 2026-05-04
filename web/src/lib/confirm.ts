// confirm() 替代:Wails 的 WebView 默认禁 window.confirm/alert/prompt
// (WKWebView 的 UIDelegate 会吞掉这些调用避免 JS 阻塞 UI 线程),结果页面里
// if (!confirm('...')) return 永远走"取消"分支。
//
// 这里用一个纯 DOM 的 promise 版本:动态插入一个 modal,用户点按钮后 resolve 布尔。
// 不依赖 Vue 组件,任何地方都能 await confirmDialog({...}) 就拿结果。
// 配色/圆角走 CSS 变量,跟 design.css 体系一致。

export interface ConfirmOptions {
  message: string
  title?: string
  confirmText?: string
  cancelText?: string
  /** 危险操作(红色按钮)。删 / 清空 / reset 之类场景。 */
  danger?: boolean
  /**
   * 控制"安全默认"落在哪一侧。决定:
   *   - 哪个按钮自动获得焦点(Enter 命中它)
   *   - Esc / 点遮罩外的"取消"语义对齐到哪一侧
   * 默认 'confirm'(老行为,Enter = 确认)。
   * 危险操作建议设 'cancel' —— 让"继续 / 不删"是默认动作,
   * 用户必须显式点红色按钮才能执行破坏性操作。
   */
  defaultAction?: 'confirm' | 'cancel'
}

export function confirmDialog(opts: ConfirmOptions): Promise<boolean> {
  return new Promise((resolve) => {
    const defaultAction = opts.defaultAction || 'confirm'
    const mask = document.createElement('div')
    mask.className = 'tshoot-confirm-mask'
    mask.innerHTML = `
      <div class="tshoot-confirm-box" role="dialog" aria-modal="true">
        ${opts.title ? `<div class="tshoot-confirm-title">${escapeHTML(opts.title)}</div>` : ''}
        <div class="tshoot-confirm-msg">${escapeHTML(opts.message)}</div>
        <div class="tshoot-confirm-actions">
          <button class="tshoot-confirm-btn tshoot-confirm-cancel" data-act="cancel">${escapeHTML(opts.cancelText || '取消')}</button>
          <button class="tshoot-confirm-btn ${opts.danger ? 'tshoot-confirm-danger' : 'tshoot-confirm-primary'}" data-act="ok">${escapeHTML(opts.confirmText || '确定')}</button>
        </div>
      </div>
    `
    const cleanup = (answer: boolean) => {
      document.removeEventListener('keydown', onKey)
      mask.remove()
      resolve(answer)
    }
    // Esc / 点遮罩外的"取消"语义跟着 defaultAction 走 ——
    // 默认安全方在 cancel 时,Esc / 点外应当落到 cancel 一侧(返回 false);
    // 默认安全方在 confirm 时,Esc / 点外其实没有"安全侧",仍按老行为返回 false。
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') cleanup(false) // 仍是 false;defaultAction='cancel' 时 false=安全侧 ✓
      else if (e.key === 'Enter') cleanup(defaultAction === 'confirm') // Enter 命中默认动作
    }
    mask.addEventListener('click', (e) => {
      const t = e.target as HTMLElement
      if (t === mask) { cleanup(false); return } // 点遮罩 = false(同 Esc)
      const btn = t.closest('button[data-act]') as HTMLButtonElement | null
      if (btn) cleanup(btn.dataset.act === 'ok')
    })
    document.addEventListener('keydown', onKey)
    document.body.appendChild(mask)
    // 自动聚焦到默认动作那侧的按钮:Enter 默认命中它,视觉上也是高亮态。
    const focusSel = defaultAction === 'cancel' ? '[data-act="cancel"]' : '[data-act="ok"]'
    const focusBtn = mask.querySelector(focusSel) as HTMLElement | null
    focusBtn?.focus()
  })
}

// 转义五个 HTML 元字符。当前调用点都在文本节点(.tshoot-confirm-msg / button text),
// 漏掉 " ' 不会立刻撞 XSS,但万一未来把 escapeHTML 用到 attribute(如 title="${...}")
// 就裸了 —— 现在收紧到位,把表面缩到 0,避免误用。
function escapeHTML(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

// 样式按需注入到 <head>;只注一次
if (typeof document !== 'undefined' && !document.getElementById('tshoot-confirm-styles')) {
  const style = document.createElement('style')
  style.id = 'tshoot-confirm-styles'
  style.textContent = `
.tshoot-confirm-mask {
  position: fixed; inset: 0; z-index: 99999;
  background: rgba(15, 23, 42, 0.45);
  display: flex; align-items: center; justify-content: center;
  padding: 24px;
  /* Wails 启动动画期间遮罩一瞬闪掉 */
  animation: tshoot-confirm-fadein 0.12s ease;
}
@keyframes tshoot-confirm-fadein { from { opacity: 0; } to { opacity: 1; } }
.tshoot-confirm-box {
  background: #fff;
  border-radius: 10px;
  padding: 22px 24px 18px;
  max-width: 420px; width: 100%;
  box-shadow: 0 10px 30px rgba(15, 23, 42, 0.2), 0 2px 6px rgba(15, 23, 42, 0.1);
  font-family: inherit;
}
.tshoot-confirm-title {
  font-size: 15px; font-weight: 600; color: #0f172a; margin-bottom: 10px;
}
.tshoot-confirm-msg {
  font-size: 13px; color: #334155; line-height: 1.6; margin-bottom: 18px;
  white-space: pre-line;
}
.tshoot-confirm-actions {
  display: flex; justify-content: flex-end; gap: 8px;
}
.tshoot-confirm-btn {
  font-family: inherit; font-size: 13px; font-weight: 500;
  padding: 8px 16px; border-radius: 6px; cursor: pointer;
  border: 1px solid #cbd5e1; background: #fff; color: #334155;
  transition: background 0.15s, border-color 0.15s;
}
.tshoot-confirm-btn:hover { background: #f1f5f9; }
.tshoot-confirm-btn:focus { outline: 2px solid #3b82f6; outline-offset: 1px; }
.tshoot-confirm-primary { background: #0f172a; border-color: #0f172a; color: #fff; }
.tshoot-confirm-primary:hover { background: #1e293b; border-color: #1e293b; }
.tshoot-confirm-danger  { background: #dc2626; border-color: #dc2626; color: #fff; }
.tshoot-confirm-danger:hover  { background: #b91c1c; border-color: #b91c1c; }
  `
  document.head.appendChild(style)
}

/** 危险操作快捷方式:红按钮 + 默认焦点在"取消",防止 Enter 误删。
 * 调用方:`if (await confirmDelete('删 X 不可恢复,继续?')) { ... }` */
export function confirmDelete(message: string, title?: string): Promise<boolean> {
  return confirmDialog({ message, title, danger: true, defaultAction: 'cancel' })
}
