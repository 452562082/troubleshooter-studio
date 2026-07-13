<script setup lang="ts">
import { computed, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { EventsOn } from '../wailsjs/runtime/runtime'
import ToastContainer from './components/ToastContainer.vue'
import { ackIncidentWorkflowReminder, listPendingIncidentWorkflowReminders, type WorkflowReminder } from './lib/bridge'
import { setupGlobalLogBridges, useLogStore, pushLog } from './lib/logStore'
import { toast } from './lib/toast'
// Vite URL import:assets/app-icon.svg 会被打进 bundle,<img src> 直接用。
// 用 app-icon(方形,1024×1024 viewBox) 而不是 logo.svg(宽 780×220)当侧边栏品牌
// 标记——侧边栏宽 220px,方形 icon 挤一下更合适。
import brandIcon from './assets/app-icon.svg'

const route = useRoute()
const currentPath = computed(() => route.path)

// 全局日志收集:install:log / analyze:log 等事件桥接进 logStore,所有页面都能往里塞,
// LogsPage 统一展示。App 启动挂一次。
let unlistenWorkflowReminders: (() => void) | undefined
const handledWorkflowReminders = new Set<string>()

async function handleWorkflowReminder(reminder: WorkflowReminder) {
  if (!reminder?.reservation_key || handledWorkflowReminders.has(reminder.reservation_key)) return
  handledWorkflowReminders.add(reminder.reservation_key)
  try {
    toast.info(`Bug ${reminder.bug_id || ''} 已等待人工部署超过 24 小时`)
  } catch (error) {
    handledWorkflowReminders.delete(reminder.reservation_key)
    pushLog('system', 'warn', `故障闭环提醒展示失败: ${error instanceof Error ? error.message : String(error)}`)
    return
  }
  try {
    await ackIncidentWorkflowReminder({ case_id: reminder.case_id, reservation_key: reminder.reservation_key, delivery_attempt: reminder.delivery_attempt, actor_id: 'desktop-root' })
  } catch (error) {
    handledWorkflowReminders.delete(reminder.reservation_key)
    pushLog('system', 'warn', `故障闭环提醒确认失败: ${error instanceof Error ? error.message : String(error)}`)
  }
}

onMounted(async () => {
  setupGlobalLogBridges()
  unlistenWorkflowReminders = EventsOn('incident-workflow:reminder', (reminder: WorkflowReminder) => { void handleWorkflowReminder(reminder) })
  try {
    for (const reminder of await listPendingIncidentWorkflowReminders()) await handleWorkflowReminder(reminder)
  } catch (error) {
    pushLog('system', 'warn', `读取待确认故障闭环提醒失败: ${error instanceof Error ? error.message : String(error)}`)
  }
})

onUnmounted(() => {
  unlistenWorkflowReminders?.()
  unlistenWorkflowReminders = undefined
})
// 给 main.ts 里的 errorHandler / window.error / unhandledrejection 留个钩子,
// 它们触发时除了红 banner 还把错误塞进 logStore —— 即便某页面白屏,侧栏「日志」永远
// 可点(侧栏不进 keep-alive 子树),用户切过去就能看到完整堆栈 + 时间 + 当前路由。
;(window as any).__tshootPushLog = (level: 'error' | 'warn' | 'info', msg: string) => {
  try { pushLog('system', level, msg, { route: window.location.hash }) } catch { /* logStore 自己出错就别再循环 */ }
}

// 日志条数 —— 侧栏"日志"项右侧小徽章显示,让用户看到有新内容产生
const { count: logCount } = useLogStore()

// 侧边栏分主路径 + 诊断工具两档。诊断工具(YAML 沙盒 / 代码扫描 / 日志)放主路径
// 后面让新用户视线先扫过去,不进诊断也无感。路径本身没有视觉分组,只是顺序靠后;
// 将来需要的话再加分隔线。
//
// 职责对齐(2026-04-30 重新切分,两者不再功能重叠):
//   YAML 沙盒  → 操作 yaml 文件:验证 / 健康检查 / 干跑生成 / 产物预览(看会出什么 skill)
//   代码扫描  → 操作代码仓库:扫码识别 service_names / 配置中心 finding,**结果可"应用回 yaml"**
//                让两个工具形成闭环 — 扫码 → 应用 → 沙盒验证 → 部署
const navItems = [
  { path: '/', icon: '🏠', label: '首页', desc: '概览 + 下一步推荐' },
  { path: '/bots', icon: '🤖', label: '已装机器人', desc: '管理已部署机器人,可重部 / 卸载' },
  { path: '/bugs', icon: '🐞', label: 'Bug 工单', desc: '同步工单平台，查看完整 Bug 详情' },
  { path: '/incidents', icon: '🔁', label: '故障闭环', desc: '选择 Bug，完成验证、排障、修复和回归' },
  { path: '/init', icon: '🧙', label: '创建向导', desc: '一步步创建一个新机器人' },
  // ── 诊断工具(下面几项) ──
  { path: '/editor', icon: '📝', label: 'YAML 沙盒', desc: '验证 yaml + 干跑生成 + 预览产物' },
  { path: '/analyze', icon: '🔍', label: '代码扫描', desc: '扫仓库反推服务 / 配置,回填 yaml' },
  { path: '/logs', icon: '📜', label: '日志', desc: '全工作台过程日志' },
]
</script>

<template>
  <div class="layout">
    <aside class="sidebar">
      <div class="sidebar-header">
        <img :src="brandIcon" class="sidebar-logo" alt="Troubleshooter Studio" />
        <div class="sidebar-title">AI 排障机器人工作台</div>
        <div class="sidebar-subtitle">troubleshooter-studio</div>
      </div>
      <nav>
        <router-link
          v-for="item in navItems"
          :key="item.path"
          :to="item.path"
          class="nav-link"
          :class="{ active: currentPath === item.path }"
        >
          <span class="nav-icon">{{ item.icon }}</span>
          <span class="nav-text">
            <span class="nav-label">{{ item.label }}</span>
            <span class="nav-desc">{{ item.desc }}</span>
          </span>
          <span v-if="item.path === '/init'" class="nav-badge">推荐</span>
          <span
            v-else-if="item.path === '/logs' && logCount > 0"
            class="nav-badge nav-badge-count"
          >{{ logCount > 999 ? '999+' : logCount }}</span>
        </router-link>
      </nav>
      <div class="sidebar-footer">
        <div class="sidebar-tip">💡 新用户？从「创建向导」开始</div>
      </div>
    </aside>
    <main class="content">
      <!-- 顶部 28px 拖动条:跟 sidebar 顶部留白对齐,让 traffic-lights 右边的整条
           白色区域都能拖窗口。绝对定位浮在内容上,但 z-index 低于交互元素,
           真有 input/button 的位置会被 no-drag 接管。 -->
      <div class="content-drag-strip" />
      <!-- keep-alive 缓存所有页面让切回时秒开。Init 不缓存 —— 用户在首页"清空重开"
           时要求一份全新草稿,缓存的 InitPage 实例里 reactive state 会让 reset 不到位
           (localStorage 清了但 setup 已经跑过)。每次进 /init 重 mount → setup → 读
           最新 localStorage → 拿到正确状态(干净 / 草稿)。重 mount 的代价 ms 级,可接受。 -->
      <router-view v-slot="{ Component, route }">
        <keep-alive :exclude="['InitPage']">
          <component :is="Component" :key="route.path === '/init' ? route.fullPath : route.path" />
        </keep-alive>
      </router-view>
    </main>
    <!-- 全局 toast,右上角浮窗,按需 import { toast } from '@/lib/toast' 推 -->
    <ToastContainer />
  </div>
</template>

<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
html, body, #app { height: 100%; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'PingFang SC', 'Microsoft YaHei', sans-serif; }

.layout { display: flex; height: 100%; }

.sidebar {
  width: 220px; min-width: 220px; background: #1e293b; color: #e2e8f0;
  display: flex; flex-direction: column; padding: 0;
  /* macOS title bar 走 HiddenInset:traffic lights 浮在 sidebar 顶部,
     给 38px 留白避让红黄绿按钮 + 跟 .content-drag-strip 对齐。整个 sidebar 设为拖动区
     (Wails v2 标准写法),交互子元素再用 no-drag 排除 —— 跟 Electron 的
     -webkit-app-region 概念类似。 */
  padding-top: 38px;
  --wails-draggable: drag;
}
/* 子元素中可点击 / 可输入的部分必须 no-drag,否则点击会被拖动手势吃掉 */
.sidebar-header,
.sidebar nav,
.sidebar .nav-link,
.sidebar-footer {
  --wails-draggable: no-drag;
}
.sidebar-header {
  padding: 12px 18px 14px; border-bottom: 1px solid #334155;
  display: flex; flex-direction: column; align-items: flex-start; gap: 4px;
}
.sidebar-logo {
  width: 44px; height: 44px; border-radius: 10px; margin-bottom: 4px;
  /* svg 里有深色 backdrop;侧边栏也是深色,靠阴影跟底色拉开 */
  box-shadow: 0 2px 8px rgba(59,130,246,0.25), 0 0 0 1px rgba(148,163,184,0.12);
}
.sidebar-title { font-size: 15px; font-weight: 700; color: #f8fafc; line-height: 1.3; }
.sidebar-subtitle { font-size: 11px; color: #64748b; font-family: monospace; }

nav { display: flex; flex-direction: column; padding: 8px 0; flex: 1; }

.nav-link {
  display: flex; align-items: center; gap: 10px;
  padding: 10px 16px; color: #94a3b8; text-decoration: none; font-size: 13px;
  transition: background 0.15s, color 0.15s; position: relative;
}
.nav-link:hover { background: #334155; color: #e2e8f0; }
.nav-link.active { background: #3b82f6; color: #fff; }
.nav-link.active .nav-desc { color: rgba(255,255,255,0.7); }

.nav-icon { font-size: 16px; flex-shrink: 0; width: 22px; text-align: center; }
.nav-text { display: flex; flex-direction: column; }
.nav-label { font-weight: 600; font-size: 13px; }
.nav-desc { font-size: 10px; color: #64748b; margin-top: 1px; }
.nav-badge {
  position: absolute; right: 12px; top: 50%; transform: translateY(-50%);
  background: #f59e0b; color: #1e293b; font-size: 9px; font-weight: 700;
  padding: 1px 6px; border-radius: 8px;
}
.nav-badge-count { background: #64748b; color: #f1f5f9; min-width: 22px; text-align: center; }
.nav-link.active .nav-badge-count { background: rgba(255,255,255,0.3); color: #fff; }

.sidebar-footer {
  padding: 12px 16px; border-top: 1px solid #334155;
}
.sidebar-tip { font-size: 11px; color: #64748b; }

.content { flex: 1; background: #fff; padding: 32px; overflow-y: auto; }
/* 顶部 28px 透明拖动条:固定定位在窗口顶部右侧(sidebar 之外),
   --wails-draggable: drag 让该区域成为拖动手柄。z-index 9999 确保在所有内容
   之上但低于 toast / modal(它们用更高 z-index)。 */
.content-drag-strip {
  position: fixed; top: 0; left: 220px; right: 0;
  height: 38px;                /* 覆盖到 traffic-lights 按钮整体高度,跟 sidebar 顶部留白对齐 */
  --wails-draggable: drag;
  z-index: 9999;
  background: transparent;
}
</style>
