import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'

const app = createApp(App)

// 桌面 app 模式下 WebKit devtools 默认关，prod build Vue 默认吞错，
// 遇到静默白屏很难排查。这里把所有错误拉出来直接显示在页面顶部，方便 debug。
function showError(msg: string) {
  let banner = document.getElementById('__err_banner__')
  if (!banner) {
    banner = document.createElement('div')
    banner.id = '__err_banner__'
    banner.style.cssText =
      'position:fixed;top:0;left:0;right:0;z-index:99999;background:#991b1b;color:#fff;' +
      'padding:10px 14px;font:12px/1.4 monospace;white-space:pre-wrap;max-height:40vh;overflow:auto;'
    document.body.appendChild(banner)
  }
  banner.textContent += msg + '\n\n'
}

app.config.errorHandler = (err, _vm, info) => {
  showError(`[vue] ${info}\n${String((err as Error)?.stack || err)}`)
}
window.addEventListener('error', (e) => {
  showError(`[window] ${e.message}\n  at ${e.filename}:${e.lineno}:${e.colno}`)
})
window.addEventListener('unhandledrejection', (e) => {
  showError(`[promise] ${String(e.reason?.stack || e.reason)}`)
})

app.use(createPinia())
app.use(router)
app.mount('#app')
