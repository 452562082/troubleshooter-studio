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
// 存储:单例 reactive,默认保留 2000 条 FIFO。**持久化到 localStorage** —— InitPage 渲染
// 异常 / 用户切到别的页面 / 关 app 重开 都不丢,排障线索可以事后回看。
//   STORAGE_KEY:tshoot-logs-v1
//   写入策略:debounce 500ms 批量落盘,避免每条日志都触发 setItem(高频日志会卡)
//   恢复策略:启动时一次性读回到 state.entries,nextID 接续最大 id+1

import { reactive, computed, readonly } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'

const STORAGE_KEY = 'tshoot-logs-v1'

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

// 启动时尝试从 localStorage 拉回上次会话的日志;失败 / 解析不出来都安静兜底成空 state。
function loadInitialState(): { entries: LogEntry[]; nextID: number } {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return { entries: [], nextID: 1 }
    const data = JSON.parse(raw)
    if (!data || !Array.isArray(data.entries)) return { entries: [], nextID: 1 }
    // 防御:落盘时丢字段 / id 异常 → 跳过那条,接续 nextID
    const entries: LogEntry[] = []
    let maxID = 0
    for (const e of data.entries) {
      if (!e || typeof e.message !== 'string' || typeof e.ts !== 'number') continue
      const id = typeof e.id === 'number' ? e.id : ++maxID
      if (id > maxID) maxID = id
      entries.push({
        id,
        ts: e.ts,
        source: (e.source as LogSource) || 'system',
        level: (e.level as LogLevel) || 'info',
        message: e.message,
        meta: (e.meta && typeof e.meta === 'object') ? e.meta : undefined,
      })
    }
    // 越界保护:历史可能写入过 > MAX_ENTRIES,启动时一次性裁掉
    if (entries.length > MAX_ENTRIES) entries.splice(0, entries.length - MAX_ENTRIES)
    return { entries, nextID: maxID + 1 }
  } catch {
    return { entries: [], nextID: 1 }
  }
}

// 单一内存状态;reactive 让页面可响应。初值从 localStorage 恢复(跨会话不丢日志)
const state = reactive<{ entries: LogEntry[]; nextID: number }>(loadInitialState())

// 落盘 debounce:每条日志都 setItem 太昂贵(MAX_ENTRIES=2000 时 JSON 序列化 + IO),
// 累积 500ms 一次性写。配合 quota 兜底:超了就先砍前 500 条再重试。
let persistTimer: ReturnType<typeof setTimeout> | null = null
function schedulePersist() {
  if (persistTimer) return
  persistTimer = setTimeout(() => {
    persistTimer = null
    persistNow()
  }, 500)
}
function persistNow() {
  try {
    const payload = JSON.stringify({ entries: state.entries, nextID: state.nextID })
    localStorage.setItem(STORAGE_KEY, payload)
  } catch (e: any) {
    // quota 失败:砍前一半重试;还失败就放弃这次写,保留内存中数据
    if (state.entries.length > 200) {
      state.entries.splice(0, Math.floor(state.entries.length / 2))
      try {
        localStorage.setItem(STORAGE_KEY, JSON.stringify({ entries: state.entries, nextID: state.nextID }))
      } catch { /* 还是不行,放弃,内存中保留即可 */ }
    }
  }
}
// 退出 / 切走页面时强制 flush 一次,避免 debounce 排程错过
if (typeof window !== 'undefined') {
  window.addEventListener('beforeunload', () => {
    if (persistTimer) {
      clearTimeout(persistTimer)
      persistTimer = null
      persistNow()
    }
  })
}

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
  schedulePersist()
}

/** 清空(UI 上「清空」按钮用) */
export function clearLogs(): void {
  state.entries.splice(0, state.entries.length)
  // 立刻落盘清空状态,避免清空后立马关 app 又被恢复
  if (persistTimer) {
    clearTimeout(persistTimer)
    persistTimer = null
  }
  try {
    localStorage.removeItem(STORAGE_KEY)
  } catch { /* ignore */ }
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
//
// 特殊处理 "N pass / M warn / K fail" 这种 summary 格式(self-test / batch report 常见):
// K=0 表示"没失败"是好消息,但行里有 "fail" 字面会被关键字匹配误判 error。
// 先抽数字 K,K=0 直接降级 warn(有 ⚠) / info(没 ⚠),不进 error 分支。
function detectLevel(line: string): LogLevel {
  const s = line.toLowerCase()
  // 抽 "K fail" 的 K(没有 fail 关键字时 failCount=null)
  const m = s.match(/(\d+)\s*(?:fail|✗|失败)/)
  const failCount = m ? parseInt(m[1], 10) : null
  if (failCount === 0) {
    // summary 格式 0 失败 → 按 warn/info 级别走,不进 error
    if (/\b(warn|warning|警告)\b/.test(s) || s.includes('⚠')) return 'warn'
    return 'info'
  }
  if (/\b(error|fail|失败|错误|panic)\b/.test(s) || s.includes('✗')) return 'error'
  if (/\b(warn|warning|警告)\b/.test(s) || s.includes('⚠')) return 'warn'
  if (/\bdebug\b/.test(s)) return 'debug'
  return 'info'
}
