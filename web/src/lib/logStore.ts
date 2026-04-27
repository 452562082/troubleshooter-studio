// logStore 收集全工作台的"过程性日志": CCHub 预加载、install.sh 安装、Analyze 扫描、
// 原生 chat 错误等等。页面 UI 只展示"结果摘要" + 一个「查看日志」入口,细节一律进这里,
// 避免把 diagnostic notes / 长错误信息堆到 wizard 等关键路径的主 UI 上。
//
// 入口:
//   pushLog(source, level, message, meta?)  // 手动塞一条(Step 5 CCHub 成功/失败都走这里)
//   Wails 事件订阅在 setupGlobalLogBridges() 里一次性挂,App.vue 启动时 import 即可;
//   install:log / analyze:log 的每行都自动入库,同时保留各页面自己的 EventsOn 监听(它们
//   还需要本地显示 running 状态)。
//
// 存储:单例 reactive ref,全局 import { useLogStore } 就能读/写;默认保留 2000 条,
// 超了 FIFO 丢旧(日志页面关不常开,没必要无限增长)。内存实现,不进 localStorage ——
// 日志是"本会话瞬态",关 app 就失效;需要排障的关键信息应落到后端文件日志,这个只是 UI。

import { reactive, computed, readonly } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'

export type LogLevel = 'info' | 'warn' | 'error' | 'debug'
// source 取值固定集合,UI 过滤下拉用;新增来源时在这里加
export type LogSource =
  | 'cchub'       // 配置中心预加载(Step 5)
  | 'install'     // install.sh 跑
  | 'analyze'     // 仓库分析
  | 'system'      // 其他通用

export interface LogEntry {
  id: number            // 自增,Vue key 用
  ts: number            // unix ms
  source: LogSource
  level: LogLevel
  message: string       // 一行文本(换行的多行消息会被拆开)
  meta?: Record<string, unknown>  // 结构化上下文(如 env.id、source file 等)
}

const MAX_ENTRIES = 2000

// 单一内存状态;reactive 让页面可响应
const state = reactive<{ entries: LogEntry[]; nextID: number }>({
  entries: [],
  nextID: 1,
})

let bridgesInstalled = false

/** 推一条日志;`message` 含换行时会拆成多条,保持 1 行 1 entry 便于过滤/复制 */
export function pushLog(
  source: LogSource,
  level: LogLevel,
  message: string,
  meta?: Record<string, unknown>,
): void {
  if (!message) return
  const lines = message.split(/\r?\n/).filter(l => l.length > 0)
  for (const line of lines) {
    state.entries.push({
      id: state.nextID++,
      ts: Date.now(),
      source,
      level,
      message: line,
      meta,
    })
  }
  // FIFO 清旧,防止内存无限增长
  if (state.entries.length > MAX_ENTRIES) {
    state.entries.splice(0, state.entries.length - MAX_ENTRIES)
  }
}

/** 清空(UI 上「清空」按钮用) */
export function clearLogs(): void {
  state.entries.splice(0, state.entries.length)
}

/** 给 UI 取只读视图 */
export function useLogStore() {
  return {
    entries: readonly(state.entries),
    count: computed(() => state.entries.length),
  }
}

/** 挂全局 Wails event 桥接 —— App.vue 启动时调一次。
 *  install:log / analyze:log 是 Go 侧每行 stdout 发一次的,这里复制一份进日志库;
 *  原页面的 EventsOn 继续存在(它们还要做本地 UI 滚动 / 状态切换)。 */
export function setupGlobalLogBridges(): void {
  if (bridgesInstalled) return
  bridgesInstalled = true
  EventsOn('install:log', (line: string) => {
    pushLog('install', detectLevel(line), line)
  })
  EventsOn('analyze:log', (line: string) => {
    pushLog('analyze', detectLevel(line), line)
  })
}

// 根据行文本简单判别等级(Go 侧日志格式不统一,UI 层尽力猜;猜不到算 info)
function detectLevel(line: string): LogLevel {
  const s = line.toLowerCase()
  if (/\b(error|fail|失败|错误|panic)\b/.test(s) || s.includes('✗')) return 'error'
  if (/\b(warn|warning|警告)\b/.test(s) || s.includes('⚠')) return 'warn'
  if (/\bdebug\b/.test(s)) return 'debug'
  return 'info'
}
