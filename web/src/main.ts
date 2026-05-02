import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './assets/design.css'

const app = createApp(App)

// 桌面 app 模式下 WebKit devtools 默认关，prod build Vue 默认吞错，
// 遇到静默白屏很难排查。所有错误都做三件事:
//  1) 顶部红 banner(用户白屏时第一眼看到,带"复制 / 收起"按钮)
//  2) 推到 logStore.system 频道(切到「日志」页能看完整堆栈 + 时间 + 路由)
//  3) console.error(devtools 开着的话同步看)
//
// 切回某页面又白屏的场景:渲染异常 → onErrorCaptured 截 → InitPage 自身的子树挂掉
// 但 banner 仍保留,用户能照着 banner 截图给我定位到具体哪行。
function showError(msg: string) {
  let banner = document.getElementById('__err_banner__')
  if (!banner) {
    banner = document.createElement('div')
    banner.id = '__err_banner__'
    banner.style.cssText =
      'position:fixed;top:0;left:0;right:0;z-index:99999;background:#991b1b;color:#fff;' +
      'padding:8px 14px;font:11px/1.4 monospace;white-space:pre-wrap;max-height:40vh;overflow:auto;' +
      'border-bottom:2px solid #fbbf24;'

    // 顶部工具栏:复制 / 收起 / 关闭
    const bar = document.createElement('div')
    bar.style.cssText = 'display:flex;gap:8px;justify-content:flex-end;margin-bottom:6px;'
    const mkBtn = (label: string, fn: () => void) => {
      const b = document.createElement('button')
      b.textContent = label
      b.style.cssText = 'background:#fff;color:#991b1b;border:0;padding:2px 10px;border-radius:3px;cursor:pointer;font:11px monospace;font-weight:600;'
      b.onclick = fn
      return b
    }
    bar.appendChild(mkBtn('📋 复制全文', () => {
      const body = document.getElementById('__err_banner_body__')
      if (body) navigator.clipboard?.writeText(body.textContent || '').catch(() => {})
    }))
    bar.appendChild(mkBtn('折叠', () => {
      const body = document.getElementById('__err_banner_body__')
      if (body) body.style.display = body.style.display === 'none' ? 'block' : 'none'
    }))
    bar.appendChild(mkBtn('✕ 关闭', () => {
      banner!.remove()
    }))
    banner.appendChild(bar)

    const body = document.createElement('div')
    body.id = '__err_banner_body__'
    banner.appendChild(body)

    document.body.appendChild(banner)
  }
  const body = document.getElementById('__err_banner_body__')
  if (body) {
    const ts = new Date().toTimeString().slice(0, 8)
    body.textContent = `[${ts}] ${msg}\n\n` + (body.textContent || '')
  }
  // 同步推到 logStore(App.vue onMounted 把钩子挂在 window.__tshootPushLog)
  try { (window as any).__tshootPushLog?.('error', msg) } catch { /* 不要因为推日志再抛错 */ }
  console.error('[showError]', msg)
}

app.config.errorHandler = (err, _vm, info) => {
  showError(`[vue ${info}] ${String((err as Error)?.stack || err)}`)
}
window.addEventListener('error', (e) => {
  showError(`[window] ${e.message}\n  at ${e.filename}:${e.lineno}:${e.colno}`)
})
window.addEventListener('unhandledrejection', (e) => {
  showError(`[promise] ${String((e.reason as any)?.stack || e.reason)}`)
})

app.use(createPinia())
app.use(router)
app.mount('#app')
