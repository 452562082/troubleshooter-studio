// bridge/shared.ts —— 跨子文件共享的小工具:isDesktop 探测 wails binding 是否在场。
// 顶层 bridge.ts 也 export isDesktop(re-export 自这里),老调用方零改动。
export function isDesktop(): boolean {
  return typeof window !== 'undefined' && window.go != null
}
