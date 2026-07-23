<script setup lang="ts">
// BotCard —— BotsPage 单个已装机器人卡片。
// 头(target + tshoot 版本徽章)+ 名/ID/path + 环境/仓库/skill 计数 + 底部 footer:
//   - 高频:🩺 诊断 / 📂 浏览工作目录
//   - 低频:⋯ 下拉(♻ 重新生成 / ✎ 编辑 / ⇩ 导出 / 🗑 卸载)
// 折叠面板:Doctor 诊断结果 + 编辑器(yaml + 预演 + 应用)。
//
// 父端持有 bots 列表 + 各种 per-card reactive map(regenState / doctorState 等),
// 把单卡所需切片透下来,本组件仅渲染 + emit 触发。

import { computed, ref, watchEffect } from 'vue'
import type { ApplyResult, DiscoveredBot } from '../lib/bridge'

interface DoctorIssue {
  severity: string
  category: string
  target: string
  message: string
  suggest?: string
}
interface DoctorEntry {
  loading: boolean
  issues?: DoctorIssue[]
  err?: string
  open?: boolean
  scannedRepoPaths?: Record<string, string>
}
interface ApplyEntry {
  loading: boolean
  result?: ApplyResult
  err?: string
  mode?: 'dry' | 'real'
}

const props = defineProps<{
  bot: DiscoveredBot
  /** Doctor 诊断 state slice;undefined = 还没跑过 */
  doctor?: DoctorEntry
  /** ♻ 重新生成按钮的 loading 态;undefined = 没在跑 */
  regenLoading?: boolean
  /** 🗑 卸载按钮的 loading 态 */
  uninstallLoading?: boolean
  /** 编辑器面板是否展开 */
  editing: boolean
  /** Apply 编辑器的预演/应用 state */
  apply?: ApplyEntry
  /** ⋯ 更多菜单是否打开 */
  menuOpen: boolean
  /** target id → 中文 label */
  targetLabel: (t: string) => string
}>()
void props // IDE 提示

const editorDraft = defineModel<string>('editorDraft', { required: true })
const agentsOpen = ref(false)
watchEffect(() => {
  if ((props.bot.meta.internal_agents || []).length > 1) {
    agentsOpen.value = true
  }
})

const emit = defineEmits<{
  runDoctor: []
  closeDoctor: []
  openBrowser: []
  openBrowserAt: [payload: { initialPath: string; agentId: string }]
  toggleMenu: []
  closeMenu: []
  regen: []
  toggleEditor: []
  doExport: []
  uninstall: []
  /** ghost bot(disk 已删但 ~/.tshoot 还有记录)的"忘掉它"操作 */
  forgetGhost: []
  /** dryRun=true 预演 / false 真应用 */
  runApply: [dryRun: boolean]
}>()

interface AgentRow {
  id: string
  role: string
  roleLabel: string
  summary: string
  definitionRel: string
}

const agentRows = computed<AgentRow[]>(() => {
  const fromMeta = props.bot.meta.internal_agents || []
  const agents = fromMeta.length
    ? fromMeta
    : [{
        id: props.bot.meta.agent_id || props.bot.meta.system_id || 'agent',
        role: props.bot.meta.role || 'troubleshooter',
      }]
  const seen = new Set<string>()
  return agents
    .filter(a => {
      const id = (a.id || '').trim()
      if (!id || seen.has(id)) return false
      seen.add(id)
      return true
    })
    .map(a => {
      const role = (a.role || '').trim() || roleFromAgentID(a.id)
      return {
        id: a.id,
        role,
        roleLabel: roleLabel(role),
        summary: roleSummary(role),
        definitionRel: `agents/${a.id}${agentDefinitionExt.value}`,
      }
    })
})

const agentDefinitionExt = computed(() => props.bot.meta.target === 'codex' ? '.toml' : '.md')

function roleFromAgentID(id: string): string {
  const s = id.toLowerCase()
  if (s.includes('fix') || s.includes('repair')) return 'fixer'
  if (s.includes('valid') || s.includes('verif')) return 'validator'
  return 'troubleshooter'
}

function roleLabel(role: string): string {
  if (role === 'validator') return '验证 Agent'
  if (role === 'fixer') return '修复 Agent'
  if (role === 'troubleshooter') return '排障 Agent'
  return `${role} Agent`
}

function roleSummary(role: string): string {
  if (role === 'validator') return '复现、回归、采集证据'
  if (role === 'fixer') return '创建修复分支、修改代码、提交并推送'
  if (role === 'troubleshooter') return '定位根因、给出修复建议'
  return '独立执行入口'
}

function severityIcon(s: string): string {
  if (s === 'error') return '✖'
  if (s === 'warning') return '⚠'
  return 'ℹ'
}
function classForSeverity(s: string): string {
  if (s === 'error') return 'doctor-err'
  if (s === 'warning') return 'doctor-warn'
  return 'doctor-info'
}
</script>

<template>
  <article class="bot-card" :class="{ 'bot-card-ghost': bot.ghost, 'bot-card-broken': !bot.ide_available && !bot.ghost }">
    <header class="bot-head">
      <span class="bot-target" :data-target="bot.meta.target">{{ targetLabel(bot.meta.target) }}</span>
      <!-- ghost > broken > version 三档 status badge,互斥(ghost 自然意味着 broken,只显严重的那条) -->
      <span
        v-if="bot.ghost"
        class="bot-status bot-status-ghost"
        title="部署目录已不在 disk(可能被外部 rm 或清理工具删了),仅从 ~/.tshoot/config.json 记录幽灵显示。点&quot;忘掉&quot;清记录,或重新部署恢复。"
      >👻 已删除</span>
      <span
        v-else-if="!bot.ide_available"
        class="bot-status bot-status-broken"
        :title="`本机未探测到 ${targetLabel(bot.meta.target)} 二进制。机器人文件还在,但 ${targetLabel(bot.meta.target)} 启动时不会加载它。装回 IDE 即可恢复。`"
      >⚠ IDE 已卸载</span>
      <!-- "tshoot dev" 是 build 没打 git tag 时的兜底字面量,信息量为零(本地构建都长这样),
           显示反而成噪音。只在版本号是真版本号(非 dev / 空)时才渲染徽章。 -->
      <span
        v-if="bot.meta.tshoot_version && bot.meta.tshoot_version !== 'dev'"
        class="bot-ver"
      >tshoot {{ bot.meta.tshoot_version }}</span>
    </header>
    <h3 class="bot-name">{{ bot.meta.system_name || bot.meta.system_id }}</h3>
    <p class="bot-id">ID: <code>{{ bot.meta.system_id }}</code></p>
    <p class="bot-path" :title="bot.path">📁 {{ bot.path }}</p>
    <ul class="bot-stats">
      <li><strong>{{ bot.env_count }}</strong> 环境</li>
      <li><strong>{{ bot.repo_count }}</strong> 仓库</li>
    </ul>
    <section v-if="!bot.ghost && agentRows.length" class="agent-section" aria-label="机器人内置 Agent">
      <button
        type="button"
        class="agent-section-head"
        :aria-expanded="agentsOpen"
        @click="agentsOpen = !agentsOpen"
      >
        <strong>Agents</strong>
        <span>{{ agentRows.length }} 个执行入口</span>
        <span class="agent-chevron" aria-hidden="true">{{ agentsOpen ? '收起' : '展开' }}</span>
      </button>
      <div v-if="agentsOpen" class="agent-list">
        <div v-for="agent in agentRows" :key="agent.id" class="agent-row">
          <div class="agent-main">
            <div class="agent-title">
              <span class="agent-role" :data-role="agent.role">{{ agent.roleLabel }}</span>
              <strong>{{ agent.id }}</strong>
            </div>
            <div class="agent-summary">{{ agent.summary }}</div>
          </div>
          <div class="agent-actions">
            <button
              class="btn btn-agent"
              :title="`打开 ${agent.id} 的工作区`"
              @click="emit('openBrowserAt', { initialPath: agent.definitionRel, agentId: agent.id })"
            >打开</button>
          </div>
        </div>
      </div>
    </section>
    <footer class="bot-foot">
      <span class="bot-time">最近更新: {{ bot.mod_time }}</span>
      <div class="bot-actions">
        <!-- Ghost bot:disk 已不在,所有依赖文件的操作都没意义,只露"忘掉"清 ~/.tshoot 记录 -->
        <button
          v-if="bot.ghost"
          class="btn btn-regen menu-item-danger"
          :title="'清掉 ~/.tshoot/config.json 里的部署记录(disk 上已经没东西可删了)。要恢复机器人请重新部署。'"
          @click="emit('forgetGhost')"
        >🗑 忘掉它</button>
        <template v-else>
          <button
            class="btn btn-regen"
            :disabled="doctor?.loading"
            :title="'按本地仓库路径深扫,对比 yaml 声明 vs 代码实态,挑漂移给修复建议'"
            @click="emit('runDoctor')"
          >
            {{ doctor?.loading ? '诊断中…' : '🩺 诊断' }}
          </button>
          <button
            class="btn btn-regen"
            :title="'打开机器人工作目录,树形浏览 + 改 SKILL.md / 脚本做调试(不动 troubleshooter.yaml)'"
            @click="emit('openBrowser')"
          >
            📂 浏览工作目录
          </button>
          <!-- ⋯ 更多:低频/管理类操作折进下拉,省卡片版面 + 降视觉噪声 -->
          <div class="bot-more-wrap">
            <button class="btn btn-regen btn-more" :title="'更多操作'" @click.stop="emit('toggleMenu')">⋯</button>
            <div v-if="menuOpen" class="bot-menu" role="menu">
              <button
                class="menu-item"
                :disabled="regenLoading"
                :title="bot.meta.troubleshooter_yaml ? '用 tshoot.json 嵌入的 yaml 重渲产物,直接刷到活 workspace(模板派生文件按模板覆盖,config-map 人工 verified 行保留)' : 'tshoot.json 里没保存 troubleshooter_yaml,无法重新生成'"
                @click="emit('closeMenu'); emit('regen')"
              >
                {{ regenLoading ? '刷新中…' : '♻ 重新生成并刷新' }}
              </button>
              <button class="menu-item" @click="emit('closeMenu'); emit('toggleEditor')">
                {{ editing ? '收起编辑器' : '✎ 编辑配置' }}
              </button>
              <button class="menu-item" @click="emit('closeMenu'); emit('doExport')">
                {{ editing ? '⇩ 导出可部署草稿' : '⇩ 导出可部署配置' }}
              </button>
              <div class="menu-sep"></div>
              <button
                class="menu-item menu-item-danger"
                :disabled="uninstallLoading"
                :title="'卸载已部署的机器人:claude-code/cursor/codex 把 ~/.<target>/{agents,skills,scripts}/<name> 移到 ~/.Trash;openclaw 摘 agents.list + 清 creds.json'"
                @click="emit('closeMenu'); emit('uninstall')"
              >
                {{ uninstallLoading ? '卸载中…' : '🗑 卸载机器人' }}
              </button>
            </div>
          </div>
        </template>
      </div>
    </footer>

    <!-- Doctor 诊断结果:已部署机器人的 saved per-repo paths 由部署流程保证存在,
         后端自动用这份路径跑深度扫描,UI 不暴露"覆盖路径"入口(代码扫描页才需要)。 -->
    <section v-if="doctor?.open" class="doctor-panel">
      <div class="doctor-head">
        <strong>🩺 诊断结果</strong>
        <span
          v-if="doctor.scannedRepoPaths && Object.keys(doctor.scannedRepoPaths).length"
          class="doctor-mode deep"
          :title="Object.entries(doctor.scannedRepoPaths).map(([n,p]) => `${n} → ${p}`).join('\n')"
        >
          深度扫 · {{ Object.keys(doctor.scannedRepoPaths).length }} 个仓库
        </span>
        <span v-else class="doctor-mode">仅静态检查 · 没找到本地仓库路径</span>
        <div class="doctor-head-actions">
          <button class="btn btn-regen" @click="emit('closeDoctor')">收起</button>
        </div>
      </div>

      <div v-if="doctor.err" class="alert error">
        {{ doctor.err }}
      </div>
      <div v-else-if="doctor.issues?.length === 0" class="alert success">
        ✓ {{ doctor.scannedRepoPaths && Object.keys(doctor.scannedRepoPaths).length
            ? '深度扫描未发现漂移'
            : '静态检查未发现问题(本系统暂无本地仓库路径记录)' }}
      </div>
      <ul v-else-if="doctor.issues" class="doctor-list">
        <li
          v-for="(iss, i) in doctor.issues"
          :key="i"
          :class="classForSeverity(iss.severity)"
        >
          <span class="doctor-icon">{{ severityIcon(iss.severity) }}</span>
          <div class="doctor-body">
            <div class="doctor-line">
              <span class="doctor-cat">{{ iss.category }}</span>
              <span v-if="iss.target" class="doctor-target">→ {{ iss.target }}</span>
            </div>
            <div class="doctor-msg">{{ iss.message }}</div>
            <div v-if="iss.suggest" class="doctor-sug">建议:{{ iss.suggest }}</div>
          </div>
        </li>
      </ul>
    </section>

    <section v-if="editing" class="editor">
      <label class="editor-label">troubleshooter.yaml(改完先「预演」看 diff,再「应用到活 workspace」写盘 —— 仅刷新本卡所属平台)</label>
      <textarea v-model="editorDraft" class="editor-textarea" spellcheck="false" />
      <div class="editor-actions">
        <button
          class="btn"
          :disabled="apply?.loading"
          :title="'干跑:渲染产物 + 算 diff,告诉你哪些会写 / 保留 / 删除,不实际写盘'"
          @click="emit('runApply', true)"
        >
          {{ apply?.loading && apply?.mode === 'dry' ? '预演中…' : '预演' }}
        </button>
        <button
          class="btn primary"
          :disabled="apply?.loading"
          :title="'真写盘到本机器人部署目录;模板派生文件按模板覆盖,config-map 人工 verified 行保留'"
          @click="emit('runApply', false)"
        >
          {{ apply?.loading && apply?.mode === 'real' ? '应用中…' : '应用到活 workspace' }}
        </button>
      </div>
      <div v-if="apply?.result" class="apply-result">
        <div class="apply-row"><strong>写入文件:</strong>{{ apply.result.files_written }}</div>
        <div v-if="apply.result.files_removed?.length" class="apply-row removed">
          <strong>移除(陈旧产物):</strong>
          <code v-for="f in apply.result.files_removed" :key="f">{{ f }}</code>
        </div>
        <div v-if="apply.result.needs_restart_hint" class="apply-hint">
          💡 {{ apply.result.needs_restart_hint }}
        </div>
      </div>
      <div v-if="apply?.err" class="apply-err">⚠ {{ apply.err }}</div>
    </section>
  </article>
</template>

<style scoped>
.bot-card {
  background: #fff; border: 1px solid #e2e8f0; border-radius: 8px; padding: 14px 16px;
  transition: box-shadow 0.15s, border-color 0.15s;
}
.bot-card:hover { border-color: #94a3b8; box-shadow: 0 2px 8px rgba(15, 23, 42, 0.06); }
/* ghost(disk 已删) / broken(IDE 已卸载) 用整卡淡背景 + 边框颜色提醒,而不仅仅靠 status badge —
   用户在卡片列表里一眼能看出"这张卡有问题",不用挨个读 badge。 */
.bot-card-ghost   { background: #fef2f2; border-color: #fecaca; }
.bot-card-broken  { background: #fffbeb; border-color: #fde68a; }
.bot-status {
  font-size: 11px; padding: 2px 8px; border-radius: 3px; font-weight: 600;
  margin-left: 6px;
}
.bot-status-ghost  { background: #fee2e2; color: #991b1b; }
.bot-status-broken { background: #fef3c7; color: #92400e; }

.bot-head { display: flex; align-items: center; gap: 6px; margin-bottom: 8px; flex-wrap: wrap; }
.bot-target {
  font-size: 11px; padding: 2px 8px; border-radius: 3px; font-weight: 600;
  background: #e0e7ff; color: #3730a3;
}
.bot-target[data-target="openclaw"] { background: #fce7f3; color: #9f1239; }
.bot-target[data-target="claude-code"] { background: #fef3c7; color: #92400e; }
.bot-target[data-target="cursor"] { background: #dbeafe; color: #1e40af; }
.bot-target[data-target="codex"] { background: #d1fae5; color: #065f46; }
.bot-ver { font-size: 11px; color: #94a3b8; font-family: monospace; }

.bot-name { font-size: 16px; font-weight: 600; color: #0f172a; margin-bottom: 4px; }
.bot-id { font-size: 12px; color: #64748b; margin-bottom: 8px; }
.bot-id code { font-family: monospace; background: #f1f5f9; padding: 1px 4px; border-radius: 3px; }
.bot-path { font-size: 11px; color: #94a3b8; font-family: monospace; margin-bottom: 10px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.bot-stats { list-style: none; display: flex; gap: 14px; padding: 10px 0; border-top: 1px solid #f1f5f9; font-size: 12px; color: #64748b; }
.bot-stats strong { color: #0f172a; font-weight: 600; margin-right: 2px; }

.agent-section {
  padding: 8px 0;
  border-top: 1px solid #f1f5f9;
}
.agent-section-head {
  width: 100%;
  display: grid; grid-template-columns: auto 1fr auto; align-items: center; gap: 8px;
  border: none; background: transparent; padding: 0;
  font: inherit; font-size: 12px; color: #64748b;
  cursor: pointer; text-align: left;
}
.agent-section-head:hover { color: #334155; }
.agent-section-head strong { color: #0f172a; font-size: 13px; }
.agent-chevron { justify-self: end; font-size: 11px; color: #64748b; }
.agent-list { display: flex; flex-direction: column; gap: 8px; margin-top: 8px; }
.agent-row {
  display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 10px; align-items: center;
  padding: 8px 0;
  border-top: 1px dashed #e2e8f0;
}
.agent-row:first-child { border-top: none; padding-top: 0; }
.agent-main { min-width: 0; }
.agent-title { display: flex; align-items: center; gap: 8px; min-width: 0; }
.agent-title strong {
  font-size: 13px; color: #0f172a; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.agent-role {
  flex: 0 0 auto; font-size: 10px; padding: 2px 7px; border-radius: 999px;
  background: #eef2ff; color: #3730a3; font-weight: 700;
}
.agent-role[data-role="validator"] { background: #ecfdf5; color: #047857; }
.agent-summary { margin-top: 3px; font-size: 11px; color: #64748b; }
.agent-actions { display: flex; gap: 6px; }
.btn-agent {
  font-size: 11px; padding: 4px 8px; border-radius: 4px;
  background: #fff; border: 1px solid #cbd5e1; color: #334155;
}
.btn-agent:hover:not(:disabled) { background: #f1f5f9; }

.bot-foot {
  border-top: 1px solid #f1f5f9; padding-top: 8px; font-size: 11px; color: #94a3b8;
  display: flex; justify-content: space-between; align-items: center; gap: 10px;
}
.btn-regen {
  font-size: 11px; padding: 4px 10px; border-radius: 4px;
  background: #f1f5f9; border: 1px solid #cbd5e1; color: #334155;
}
.btn-regen:hover:not(:disabled) { background: #e2e8f0; }
/* ⋯ 更多菜单:外露只留高频,管理类折进来让卡片不拥挤 */
.bot-more-wrap { position: relative; }
.btn-more {
  font-size: 14px; line-height: 1; padding: 4px 10px;
}
.bot-menu {
  position: absolute; top: calc(100% + 4px); right: 0; z-index: 10;
  min-width: 140px; padding: 4px 0;
  background: #fff; border: 1px solid var(--c-line-2); border-radius: 6px;
  box-shadow: 0 6px 16px rgba(15, 23, 42, 0.12);
  display: flex; flex-direction: column;
}
.bot-menu .menu-item {
  text-align: left; padding: 7px 14px; font-size: 12px;
  border: none; background: transparent; color: var(--c-text); cursor: pointer;
  font-family: inherit;
}
.bot-menu .menu-item:hover:not(:disabled) { background: var(--c-surf-3); }
.bot-menu .menu-item:disabled { opacity: 0.5; cursor: not-allowed; }
/* 危险操作(卸载)用红色文字 + 顶上加分隔线,跟普通 menu item 视觉拉开,降低误点风险 */
.bot-menu .menu-sep { height: 1px; background: var(--c-line-2); margin: 4px 0; }
.bot-menu .menu-item-danger { color: #dc2626; }
.bot-menu .menu-item-danger:hover:not(:disabled) { background: #fef2f2; color: #b91c1c; }

.bot-actions { display: flex; gap: 6px; }

/* Doctor 诊断结果面板 */
.doctor-panel {
  margin-top: 10px; padding-top: 10px; border-top: 1px dashed var(--c-line-2);
}
.doctor-head {
  display: flex; align-items: center; gap: 10px;
  margin-bottom: 8px; font-size: var(--fs-sm); color: var(--c-ink);
  flex-wrap: wrap;
}
.doctor-mode {
  font-size: var(--fs-xs); color: var(--c-muted);
  background: var(--c-surf-3); padding: 2px 8px; border-radius: 10px;
}
.doctor-mode.deep { background: #e0e7ff; color: #3730a3; font-family: monospace; }
.doctor-head-actions { margin-left: auto; display: flex; gap: 6px; }

.doctor-list {
  list-style: none; padding: 0; margin: 0;
  display: flex; flex-direction: column; gap: 6px;
}
.doctor-list li {
  display: flex; gap: 10px; padding: 8px 10px;
  border-radius: var(--r-sm); font-size: var(--fs-xs);
}
.doctor-err  { background: var(--c-danger-bg);  border: 1px solid var(--c-danger-border); color: var(--c-danger); }
.doctor-warn { background: #fffbeb;            border: 1px solid #fde68a;             color: var(--c-warn); }
.doctor-info { background: #eff6ff;            border: 1px solid #bfdbfe;             color: #1e40af; }
.doctor-icon { font-size: 14px; flex-shrink: 0; line-height: 1.4; }
.doctor-body { flex: 1; line-height: 1.5; }
.doctor-line { font-weight: 600; margin-bottom: 2px; }
.doctor-cat { font-family: monospace; }
.doctor-target { margin-left: 4px; opacity: 0.85; }
.doctor-msg { opacity: 0.92; }
.doctor-sug { margin-top: 4px; opacity: 0.8; font-style: italic; }

.editor {
  margin-top: 12px; padding-top: 12px; border-top: 1px dashed #cbd5e1;
}
.editor-label { display: block; font-size: 11px; color: #64748b; margin-bottom: 6px; }
.editor-textarea {
  width: 100%; min-height: 240px; font-family: 'SFMono-Regular', 'Menlo', monospace;
  font-size: 11px; padding: 8px 10px; border: 1px solid #cbd5e1; border-radius: 4px;
  resize: vertical; line-height: 1.5; background: #f8fafc; color: #0f172a;
}
.editor-actions { display: flex; gap: 8px; margin-top: 8px; }

.apply-result {
  margin-top: 10px; padding: 10px 12px; background: #f0fdf4; border: 1px solid #bbf7d0;
  border-radius: 4px; font-size: 11px; color: #166534;
}
.apply-result .apply-row { margin-bottom: 4px; line-height: 1.6; }
.apply-result .apply-row.removed { color: #9a3412; }
.apply-result code { background: rgba(15, 23, 42, 0.05); padding: 1px 4px; border-radius: 2px; margin-right: 4px; font-family: inherit; }
.apply-hint { margin-top: 6px; padding-top: 6px; border-top: 1px dashed #bbf7d0; color: #166534; }
.apply-err { margin-top: 10px; padding: 8px 12px; background: #fef2f2; border: 1px solid #fecaca; border-radius: 4px; font-size: 11px; color: #991b1b; }
</style>
