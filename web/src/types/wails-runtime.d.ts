// Wails 在桌面 app 里注入 window.go(entry: main.App),浏览器/ Vite dev 下没有。
// 这里不穷举 App 方法类型(那部分已由 wails generate module 生成到 wailsjs/go/ 下),
// 只声明 window.go 存在性让 bridge.ts 的 isDesktop() 能类型安全地检查。
export {}

declare global {
  interface Window {
    go?: unknown
  }
}
