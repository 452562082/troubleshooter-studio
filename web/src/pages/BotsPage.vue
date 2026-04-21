<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import type { DiscoveredBot } from '../types/wails'

const bots = ref<DiscoveredBot[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const extraRoots = ref<string[]>([])
const newRootInput = ref('')

const isDesktop = computed(() => typeof window !== 'undefined' && !!window.go?.main?.App?.DiscoverBots)

async function scan() {
  if (!isDesktop.value) {
    error.value = '需要在桌面 app 里打开此页面（window.go 不可用）'
    return
  }
  loading.value = true
  error.value = null
  try {
    bots.value = await window.go!.main.App.DiscoverBots(extraRoots.value)
  } catch (e: any) {
    error.value = String(e?.message || e)
    bots.value = []
  } finally {
    loading.value = false
  }
}

function addRoot() {
  const v = newRootInput.value.trim()
  if (!v) return
  if (!extraRoots.value.includes(v)) extraRoots.value.push(v)
  newRootInput.value = ''
  scan()
}

function removeRoot(r: string) {
  extraRoots.value = extraRoots.value.filter((x) => x !== r)
  scan()
}

function targetLabel(t: string): string {
  const map: Record<string, string> = {
    openclaw: 'OpenClaw',
    'claude-code': 'Claude Code',
    cursor: 'Cursor',
    standalone: 'Standalone',
  }
  return map[t] ?? t
}

onMounted(scan)
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <h1>已装机器人</h1>
        <p class="subtitle">扫描本机 .tshoot.json 锚点，列出已经部署到 AI 平台的排障机器人。</p>
      </div>
      <button class="btn primary" :disabled="loading" @click="scan">
        {{ loading ? '扫描中…' : '刷新' }}
      </button>
    </header>

    <section class="roots">
      <div class="roots-head">
        <span class="roots-label">扫描路径</span>
        <span class="hint">默认扫 <code>~/.openclaw/workspace</code>。如果机器人装在 Claude Code / Cursor 项目根里，把项目路径加进来。</span>
      </div>
      <div class="root-list">
        <span class="root-item builtin">~/.openclaw/workspace <span class="tag">默认</span></span>
        <span v-for="r in extraRoots" :key="r" class="root-item">
          {{ r }}
          <button class="root-remove" @click="removeRoot(r)">×</button>
        </span>
      </div>
      <div class="root-add">
        <input
          v-model="newRootInput"
          placeholder="/path/to/project 或 ~/my-repo"
          @keyup.enter="addRoot"
        />
        <button class="btn" @click="addRoot">添加并扫描</button>
      </div>
    </section>

    <div v-if="error" class="alert error">⚠️ {{ error }}</div>
    <div v-else-if="!isDesktop" class="alert info">
      这个页面需要在桌面 app 里打开。浏览器模式暂不可用。
    </div>
    <div v-else-if="loading" class="empty">扫描中…</div>
    <div v-else-if="bots.length === 0" class="empty">
      没找到已安装的机器人。可能原因：<br />
      1) OpenClaw 工作区为空；2) 机器人装在别处（点上面「添加扫描路径」）；3) 安装时没写 <code>.tshoot.json</code>（老版本产物）
    </div>

    <div v-else class="bot-grid">
      <article v-for="b in bots" :key="b.path + b.meta.target" class="bot-card">
        <header class="bot-head">
          <span class="bot-target" :data-target="b.meta.target">{{ targetLabel(b.meta.target) }}</span>
          <span class="bot-ver">tshoot {{ b.meta.tshoot_version || '?' }}</span>
        </header>
        <h3 class="bot-name">{{ b.meta.system_name || b.meta.system_id }}</h3>
        <p class="bot-id">ID: <code>{{ b.meta.system_id }}</code></p>
        <p class="bot-path" :title="b.path">📁 {{ b.path }}</p>
        <ul class="bot-stats">
          <li><strong>{{ b.env_count }}</strong> 环境</li>
          <li><strong>{{ b.repo_count }}</strong> 仓库</li>
          <li><strong>{{ b.skill_count }}</strong> skills</li>
        </ul>
        <footer class="bot-foot">
          <span class="bot-time">最近更新：{{ b.mod_time }}</span>
        </footer>
      </article>
    </div>
  </div>
</template>

<style scoped>
.page { padding: 24px 28px; max-width: 1100px; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 20px; }
.page-header h1 { font-size: 22px; font-weight: 600; color: #0f172a; }
.subtitle { font-size: 13px; color: #64748b; margin-top: 4px; }

.btn {
  padding: 8px 16px; border: 1px solid #cbd5e1; border-radius: 6px;
  background: #fff; color: #334155; font-size: 13px; cursor: pointer;
}
.btn:hover { background: #f1f5f9; }
.btn.primary { background: #0f172a; color: #fff; border-color: #0f172a; }
.btn.primary:hover { background: #1e293b; }
.btn:disabled { opacity: 0.5; cursor: not-allowed; }

.roots { background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 8px; padding: 14px 16px; margin-bottom: 20px; }
.roots-head { display: flex; align-items: baseline; gap: 12px; margin-bottom: 10px; flex-wrap: wrap; }
.roots-label { font-weight: 600; font-size: 13px; color: #334155; }
.hint { font-size: 12px; color: #64748b; }
.hint code { background: #e2e8f0; padding: 1px 4px; border-radius: 3px; font-size: 11px; }

.root-list { display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.root-item {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 4px 10px; background: #fff; border: 1px solid #cbd5e1; border-radius: 4px;
  font-family: monospace; font-size: 12px; color: #334155;
}
.root-item.builtin { background: #ecfeff; border-color: #a5f3fc; color: #155e75; }
.root-item .tag { font-size: 10px; background: #0891b2; color: #fff; padding: 1px 5px; border-radius: 3px; font-family: inherit; }
.root-remove { background: none; border: none; color: #94a3b8; cursor: pointer; font-size: 16px; line-height: 1; padding: 0 2px; }
.root-remove:hover { color: #ef4444; }

.root-add { display: flex; gap: 8px; }
.root-add input { flex: 1; padding: 6px 10px; border: 1px solid #cbd5e1; border-radius: 4px; font-size: 13px; }

.alert { padding: 12px 14px; border-radius: 6px; font-size: 13px; margin-bottom: 16px; }
.alert.error { background: #fef2f2; border: 1px solid #fecaca; color: #991b1b; }
.alert.info { background: #eff6ff; border: 1px solid #bfdbfe; color: #1e40af; }

.empty { text-align: center; padding: 48px 24px; color: #94a3b8; font-size: 14px; line-height: 1.8; }
.empty code { background: #f1f5f9; padding: 1px 4px; border-radius: 3px; }

.bot-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 14px; }
.bot-card {
  background: #fff; border: 1px solid #e2e8f0; border-radius: 8px; padding: 14px 16px;
  transition: box-shadow 0.15s, border-color 0.15s;
}
.bot-card:hover { border-color: #94a3b8; box-shadow: 0 2px 8px rgba(15, 23, 42, 0.06); }

.bot-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.bot-target {
  font-size: 11px; padding: 2px 8px; border-radius: 3px; font-weight: 600;
  background: #e0e7ff; color: #3730a3;
}
.bot-target[data-target="openclaw"] { background: #fce7f3; color: #9f1239; }
.bot-target[data-target="claude-code"] { background: #fef3c7; color: #92400e; }
.bot-target[data-target="cursor"] { background: #dbeafe; color: #1e40af; }
.bot-target[data-target="standalone"] { background: #d1fae5; color: #065f46; }
.bot-ver { font-size: 11px; color: #94a3b8; font-family: monospace; }

.bot-name { font-size: 16px; font-weight: 600; color: #0f172a; margin-bottom: 4px; }
.bot-id { font-size: 12px; color: #64748b; margin-bottom: 8px; }
.bot-id code { font-family: monospace; background: #f1f5f9; padding: 1px 4px; border-radius: 3px; }
.bot-path { font-size: 11px; color: #94a3b8; font-family: monospace; margin-bottom: 10px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.bot-stats { list-style: none; display: flex; gap: 14px; padding: 10px 0; border-top: 1px solid #f1f5f9; font-size: 12px; color: #64748b; }
.bot-stats strong { color: #0f172a; font-weight: 600; margin-right: 2px; }

.bot-foot { border-top: 1px solid #f1f5f9; padding-top: 8px; font-size: 11px; color: #94a3b8; }
</style>
