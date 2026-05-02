<script setup lang="ts">
import { computed, nextTick, onActivated, onMounted, ref, watch } from 'vue'
import { clearLogs, useLogStore, type LogLevel, type LogSource } from '../lib/logStore'
import ChipFilterBar from '../components/ChipFilterBar.vue'
import { copyToClipboard } from '../lib/clipboard'

// 全工作台日志聚合:CCHub 预加载 / install.sh / analyze 都会往 logStore 推,
// 这里只做展示 + 过滤 + 复制 + 清空。不落 localStorage —— 会话内的过程信息,关窗就算结束。

const { entries } = useLogStore()

// 过滤器:source 多选 + level 多选 + 关键词
const sourceFilter = ref<Record<LogSource, boolean>>({
  cchub: true, install: true, analyze: true, system: true,
})
const levelFilter = ref<Record<LogLevel, boolean>>({
  info: true, warn: true, error: true, debug: true,
})
const keyword = ref('')
const autoScroll = ref(true)

const filtered = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return entries.filter(e =>
    sourceFilter.value[e.source] &&
    levelFilter.value[e.level] &&
    (kw === '' || e.message.toLowerCase().includes(kw)),
  )
})

// 每个 source 的条目数 —— 灰度 chip 上展示,让用户一眼看到哪来源最多
const sourceCounts = computed(() => {
  const acc: Record<LogSource, number> = { cchub: 0, install: 0, analyze: 0, system: 0 }
  for (const e of entries) acc[e.source]++
  return acc
})
const levelCounts = computed(() => {
  const acc: Record<LogLevel, number> = { info: 0, warn: 0, error: 0, debug: 0 }
  for (const e of entries) acc[e.level]++
  return acc
})

// 正序展示(新日志在底);新行进来时如果 autoScroll 开着,滚到底部确保最新可见
const listRef = ref<HTMLElement | null>(null)
async function scrollToBottom() {
  await nextTick()
  const el = listRef.value
  if (!el) return
  el.scrollTop = el.scrollHeight
}
watch(filtered, () => {
  if (!autoScroll.value) return
  void scrollToBottom()
}, { flush: 'post' })
// 进入 / 重新激活页面时默认滚到底,看最新日志不用手动翻
onMounted(scrollToBottom)
onActivated(scrollToBottom) // keep-alive 切回来也触发

function fmtTs(ts: number): string {
  const d = new Date(ts)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, '0')}`
}

async function copyAll() {
  const text = filtered.value
    .map(e => `[${fmtTs(e.ts)}] [${e.source}/${e.level}] ${e.message}`)
    .join('\n')
  await copyToClipboard(text)
}

const SOURCE_LABELS: Record<LogSource, string> = {
  cchub: '配置中心',
  install: '安装流程',
  analyze: '代码扫描',
  system: '其它',
}
</script>

<template>
  <div class="logs-page">
    <header class="logs-header">
      <h1>日志</h1>
      <p class="logs-sub">
        全工作台过程日志:配置中心预加载、原生安装、代码扫描、系统事件。共 {{ entries.length }} 条(过滤后 {{ filtered.length }} 条)。
      </p>
    </header>

    <div class="logs-toolbar">
      <ChipFilterBar
        label="来源"
        :keys="(Object.keys(sourceFilter) as LogSource[])"
        v-model="sourceFilter"
        :labels="SOURCE_LABELS"
        :counts="sourceCounts"
      />
      <ChipFilterBar
        label="级别"
        :keys="(Object.keys(levelFilter) as LogLevel[])"
        v-model="levelFilter"
        :counts="levelCounts"
        :item-class="(k) => 'lvl-' + k"
      />
      <div class="toolbar-group">
        <input
          v-model="keyword"
          type="text"
          placeholder="关键词过滤(message)"
          class="toolbar-input"
        />
      </div>
      <div class="toolbar-group toolbar-right">
        <label class="toolbar-chip">
          <input type="checkbox" v-model="autoScroll" /> 自动滚到底
        </label>
        <button class="btn" @click="copyAll">复制</button>
        <button class="btn btn-danger" @click="clearLogs">清空</button>
      </div>
    </div>

    <div v-if="filtered.length === 0" class="logs-empty">
      {{ entries.length === 0 ? '本会话暂无日志' : '当前过滤条件没有匹配的日志' }}
    </div>

    <div v-else ref="listRef" class="logs-list">
      <div
        v-for="e in filtered"
        :key="e.id"
        class="log-row"
        :class="'lvl-' + e.level"
      >
        <span class="log-ts">{{ fmtTs(e.ts) }}</span>
        <span class="log-src">{{ SOURCE_LABELS[e.source] }}</span>
        <span class="log-lvl">{{ e.level }}</span>
        <span class="log-msg">{{ e.message }}</span>
      </div>
    </div>
  </div>
</template>

<style>
/* 不加 scoped:ChipFilterBar 子组件复用本页 .toolbar-group / .toolbar-chip / .toolbar-count
   类名,scoped 会让样式不穿透。这些类名 LogsPage 域专属,无跨页冲突风险。 */
.logs-page { max-width: 1200px; display: flex; flex-direction: column; gap: 14px; height: 100%; }
.logs-header h1 { font-size: 24px; color: #1e293b; margin-bottom: 4px; }
.logs-sub { font-size: 12px; color: #64748b; }

.logs-toolbar {
  display: flex; flex-wrap: wrap; gap: 10px; align-items: center;
  padding: 10px 12px;
  background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 8px;
}
.toolbar-group { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
.toolbar-right { margin-left: auto; }
.toolbar-label { font-size: 11px; color: #64748b; font-weight: 600; margin-right: 2px; }
.toolbar-chip {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 3px 8px;
  background: #fff; border: 1px solid #cbd5e1; border-radius: 10px;
  font-size: 11px; color: #64748b; cursor: pointer;
  user-select: none;
}
.toolbar-chip input[type=checkbox] { width: 12px; height: 12px; margin: 0; cursor: pointer; }
.toolbar-chip.active { background: #dbeafe; border-color: #3b82f6; color: #1e40af; }
.toolbar-chip.lvl-warn.active { background: #fef3c7; border-color: #f59e0b; color: #78350f; }
.toolbar-chip.lvl-error.active { background: #fee2e2; border-color: #dc2626; color: #991b1b; }
.toolbar-chip.lvl-debug.active { background: #f3f4f6; border-color: #9ca3af; color: #4b5563; }
.toolbar-count {
  font-size: 10px; background: rgba(0,0,0,0.08); padding: 0 4px; border-radius: 8px;
}

.toolbar-input {
  padding: 4px 8px; font-size: 12px; border: 1px solid #cbd5e1; border-radius: 4px;
  background: #fff; color: #1e293b; min-width: 220px;
}
.toolbar-input:focus { outline: none; border-color: #3b82f6; }

/* 注意:.btn / .btn-danger 是全局类,这里把日志页工具栏的紧凑变体限定在 .logs-page 子树内,
   不再泄漏到其他页(如 InitPage 部署按钮),避免切到日志再切回去就被覆盖成小尺寸 */
.logs-page .btn {
  padding: 4px 12px; font-size: 12px; border: 1px solid #cbd5e1;
  background: #fff; color: #1e293b; border-radius: 4px; cursor: pointer;
}
.logs-page .btn:hover { background: #f1f5f9; }
.logs-page .btn-danger { border-color: #fca5a5; color: #b91c1c; }
.logs-page .btn-danger:hover { background: #fee2e2; }

.logs-empty {
  padding: 40px 20px; text-align: center; color: #94a3b8;
  background: #f8fafc; border: 1px dashed #cbd5e1; border-radius: 8px;
  font-size: 13px;
}

.logs-list {
  flex: 1; overflow-y: auto; min-height: 0;
  border: 1px solid #e2e8f0; border-radius: 8px;
  background: #fafafa; font-family: monospace;
}
.log-row {
  display: grid;
  grid-template-columns: 100px 80px 60px 1fr;
  gap: 10px; align-items: baseline;
  padding: 4px 12px; font-size: 12px;
  border-bottom: 1px solid #f1f5f9;
}
.log-row:hover { background: #f1f5f9; }
.log-ts { color: #94a3b8; }
.log-src { color: #3b82f6; font-weight: 500; }
.log-lvl {
  text-align: center; border-radius: 3px; padding: 0 4px;
  font-size: 10px; font-weight: 600;
  background: #e2e8f0; color: #475569;
}
.log-row.lvl-warn .log-lvl { background: #fef3c7; color: #92400e; }
.log-row.lvl-error .log-lvl { background: #fee2e2; color: #991b1b; }
.log-row.lvl-error { background: #fef2f2; }
.log-row.lvl-warn { background: #fffbeb; }
.log-msg { color: #1e293b; word-break: break-all; white-space: pre-wrap; }
</style>
