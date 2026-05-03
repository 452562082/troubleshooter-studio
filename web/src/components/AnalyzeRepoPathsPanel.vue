<script setup lang="ts">
// AnalyzeRepoPathsPanel —— AnalyzePage 的"仓库本地路径"编辑面板。
// 包括:覆盖率 banner + 每仓库 row(✓/✎/· 状态 + 路径展示 + 选目录/清除按钮)+
// 底部 footer(💾 保存到 ~/.tshoot/config.json + ☐ autoClone 兜底 + 提示)。
//
// 父端持有 yamlContent / yamlSystemID / savedRepoPaths,通过 v-model 双向绑定
// repoPathDrafts(per-repo path map)+ autoClone(boolean)。本组件内部管:
//   - showRepoPathPanel(展开/折叠)
//   - 选目录 / 批量填 / 清除 / 保存到 user config 的 4 个动作

import { computed, ref } from 'vue'
import { isDesktop, openDir, saveRepoPathsForSystem } from '../lib/bridge'
import { toast, toastError } from '../lib/toast'

const props = defineProps<{
  yamlRepoNames: string[]
  yamlSystemID: string
  savedRepoPaths: Record<string, string>
  cloneFallbackDisplay: string
  loading?: boolean
}>()

const drafts = defineModel<Record<string, string>>('drafts', { required: true })
const autoClone = defineModel<boolean>('autoClone', { required: true })

const emit = defineEmits<{
  /** drafts 落盘成功后 emit,父端可同步刷新 savedRepoPaths */
  saved: [filtered: Record<string, string>]
  error: [msg: string]
}>()

const showRepoPathPanel = ref(true)

const effectiveRepoPaths = computed<Record<string, string>>(() => {
  const out: Record<string, string> = {}
  for (const n of props.yamlRepoNames) {
    const v = (drafts.value[n] || '').trim() || (props.savedRepoPaths[n] || '').trim()
    if (v) out[n] = v
  }
  return out
})
const reposCoveredByPaths = computed(() =>
  props.yamlRepoNames.filter(n => !!effectiveRepoPaths.value[n]),
)
const reposNeedingReposRoot = computed(() =>
  props.yamlRepoNames.filter(n => !effectiveRepoPaths.value[n]),
)
const allReposCovered = computed(() =>
  props.yamlRepoNames.length > 0 && reposNeedingReposRoot.value.length === 0,
)
const draftsDirty = computed(() => {
  for (const n of props.yamlRepoNames) {
    const d = (drafts.value[n] || '').trim()
    const s = (props.savedRepoPaths[n] || '').trim()
    if (d !== s) return true
  }
  return false
})

async function pickRepoPath(repoName: string) {
  if (!isDesktop()) { emit('error', '选目录需要桌面 app 环境'); return }
  try {
    const p = await openDir(`选 ${repoName} 仓库本地目录`)
    if (p) drafts.value = { ...drafts.value, [repoName]: p }
  } catch (e: any) {
    emit('error', String(e?.message || e))
  }
}
async function batchFillFromParent() {
  if (!isDesktop()) { emit('error', '选目录需要桌面 app 环境'); return }
  try {
    const parent = await openDir('选父目录(将用 <父目录>/<repo.name> 填补所有空格)')
    if (!parent) return
    const trimmed = parent.replace(/\/+$/, '')
    const next = { ...drafts.value }
    let filled = 0
    for (const name of props.yamlRepoNames) {
      if ((next[name] || '').trim()) continue
      next[name] = `${trimmed}/${name}`
      filled++
    }
    drafts.value = next
    if (filled === 0) toast.info('所有仓库都已配置,本次没填新路径')
    else toast.success(`✓ 用 ${trimmed} 填了 ${filled} 个空仓库`)
  } catch (e: any) {
    emit('error', String(e?.message || e))
  }
}
function clearRepoPath(repoName: string) {
  const next = { ...drafts.value }
  delete next[repoName]
  drafts.value = next
}
async function saveDraftsToUserConfig() {
  if (!isDesktop()) { toast.error('保存仅在桌面 app 可用'); return }
  if (!props.yamlSystemID) { toast.error('yaml 缺 system.id,无法保存'); return }
  try {
    const filtered: Record<string, string> = {}
    for (const [k, v] of Object.entries(drafts.value)) {
      if ((v || '').trim()) filtered[k] = v.trim()
    }
    await saveRepoPathsForSystem(props.yamlSystemID, filtered)
    emit('saved', filtered)
    toast.success(`✓ 已保存 ${Object.keys(filtered).length} 个仓库路径到 ~/.tshoot/config.json`)
  } catch (e) {
    toastError('保存', e)
  }
}

defineExpose({ effectiveRepoPaths, allReposCovered, reposNeedingReposRoot })
</script>

<template>
  <div v-if="yamlRepoNames.length > 0">
    <!-- 仓库路径状态 banner -->
    <div
      class="saved-paths-banner"
      :class="allReposCovered ? 'all-covered' : reposCoveredByPaths.length > 0 ? 'partial' : 'none'"
    >
      <template v-if="allReposCovered">
        ✓ 全部 {{ yamlRepoNames.length }} 个仓库都已配置本地路径(部署时记下的 + 你刚填的),可直接跑分析。
      </template>
      <template v-else-if="reposCoveredByPaths.length > 0">
        ⓘ 已配置 {{ reposCoveredByPaths.length }}/{{ yamlRepoNames.length }} 个仓库本地路径
        ;还有 {{ reposNeedingReposRoot.length }} 个 ({{ reposNeedingReposRoot.slice(0,3).join(', ') }}{{ reposNeedingReposRoot.length>3?'…':'' }}) 没填 —— 在下方挨个选,或填父目录兜底,或勾自动 clone。
      </template>
      <template v-else>
        ⓘ 该 system 没保存仓库本地路径。可在下方"仓库本地路径"挨个选,或选父目录(repos 都在同根下)+ 选 autoClone 让后端自己 clone。
      </template>
    </div>

    <div class="repo-paths-card">
      <header class="repo-paths-card-head">
        <span class="repo-paths-title" @click="showRepoPathPanel = !showRepoPathPanel">📁 仓库本地路径</span>
        <span class="repo-paths-progress" @click="showRepoPathPanel = !showRepoPathPanel">
          <span class="repo-paths-progress-num">{{ reposCoveredByPaths.length }}</span>
          <span class="repo-paths-progress-sep">/</span>
          <span class="repo-paths-progress-total">{{ yamlRepoNames.length }}</span>
          <span class="repo-paths-progress-label">已配置</span>
        </span>
        <button
          v-if="showRepoPathPanel"
          class="btn small"
          :disabled="loading"
          @click.stop="batchFillFromParent"
          title="选一个父目录,所有空格用 <父目录>/<repo.name> 自动填(已填的不动)"
        >
          📁 批量填充…
        </button>
        <span class="repo-paths-collapse" :aria-expanded="showRepoPathPanel" @click="showRepoPathPanel = !showRepoPathPanel">{{ showRepoPathPanel ? '▴' : '▾' }}</span>
      </header>
      <div v-if="showRepoPathPanel" class="repo-paths-body">
        <div v-for="name in yamlRepoNames" :key="name" class="repo-row" :class="{ 'is-empty': !drafts[name] }">
          <span class="repo-row-status" :class="drafts[name] ? (savedRepoPaths[name] === drafts[name] ? 'saved' : 'edited') : 'empty'">
            <template v-if="drafts[name] && savedRepoPaths[name] === drafts[name]">✓</template>
            <template v-else-if="drafts[name]">✎</template>
            <template v-else>·</template>
          </span>
          <span class="repo-row-name" :title="name">{{ name }}</span>
          <input
            type="text"
            readonly
            class="repo-row-path"
            :value="drafts[name] || ''"
            :placeholder="savedRepoPaths[name] ? '已保存,点右侧改…' : '尚未配置,点右侧选目录'"
            :title="drafts[name] || savedRepoPaths[name] || ''"
          />
          <div class="repo-row-actions">
            <button
              class="icon-btn"
              :disabled="loading"
              @click="pickRepoPath(name)"
              :title="drafts[name] ? '更换目录' : '选择目录'"
              aria-label="选择目录"
            >📂</button>
            <button
              v-if="drafts[name]"
              class="icon-btn icon-btn-danger"
              :disabled="loading"
              @click="clearRepoPath(name)"
              title="清除路径(留空让父目录兜底)"
              aria-label="清除"
            >✕</button>
            <span v-else class="icon-btn-placeholder" aria-hidden="true"></span>
          </div>
        </div>

        <footer class="repo-paths-footer">
          <button
            class="btn small primary"
            :disabled="!draftsDirty || !yamlSystemID"
            :title="!yamlSystemID ? 'yaml 缺 system.id 无法保存' : (draftsDirty ? '把上面的路径表持久化到 ~/.tshoot/config.json,下次诊断 / 部署 / 分析直接复用' : '当前路径表跟已保存的一致,无需重复保存')"
            @click="saveDraftsToUserConfig"
          >
            💾 保存到本地配置
          </button>
          <label class="auto-clone-toggle" :title="autoClone ? `本机没有的仓库会浅克隆到 ${cloneFallbackDisplay}` : '勾上后,上方没填的仓库会按 yaml 里 url 自动 clone'">
            <input type="checkbox" v-model="autoClone" />
            自动 clone 缺失仓库
            <span v-if="autoClone" class="auto-clone-dest">→ {{ cloneFallbackDisplay }}</span>
          </label>
          <span class="repo-paths-footer-hint">
            不保存也能跑(仅本次会话);保存后 BotsPage 诊断 / 重新部署都能复用,免得重选。
          </span>
        </footer>
      </div>
    </div>
  </div>
</template>

<style scoped>
.saved-paths-banner {
  font-size: var(--fs-sm); padding: 8px 12px; border-radius: 6px; margin-bottom: 8px;
  border: 1px solid transparent; line-height: 1.5;
}
.saved-paths-banner.all-covered { background: #ecfdf5; color: #065f46; border-color: #a7f3d0; }
.saved-paths-banner.partial { background: #fffbeb; color: #92400e; border-color: #fde68a; }
.saved-paths-banner.none { background: #eff6ff; color: #1e40af; border-color: #bfdbfe; }

.repo-paths-card {
  border: 1px solid var(--c-border, #e2e8f0);
  border-radius: 8px;
  margin-bottom: 16px;
  background: #fff;
  overflow: hidden;
  box-shadow: 0 1px 2px rgba(15, 23, 42, 0.04);
}
.repo-paths-card-head {
  padding: 10px 16px;
  cursor: pointer;
  user-select: none;
  display: flex;
  align-items: center;
  gap: 12px;
  background: linear-gradient(180deg, #fafbfc 0%, #f4f6f8 100%);
  border-bottom: 1px solid var(--c-border, #e2e8f0);
}
.repo-paths-title { font-size: 14px; font-weight: 600; color: var(--c-ink, #0f172a); }
.repo-paths-progress {
  font-size: 12px; color: var(--c-muted, #64748b);
  display: inline-flex; align-items: baseline; gap: 2px;
}
.repo-paths-progress-num { color: #16a34a; font-weight: 600; font-size: 13px; font-variant-numeric: tabular-nums; }
.repo-paths-progress-sep { color: var(--c-muted, #94a3b8); }
.repo-paths-progress-total { color: var(--c-muted, #64748b); font-variant-numeric: tabular-nums; }
.repo-paths-progress-label { margin-left: 4px; }
.repo-paths-collapse {
  margin-left: auto;
  color: var(--c-muted, #64748b);
  font-size: 14px;
  width: 20px; text-align: center;
}
.repo-paths-body { padding: 8px 12px 12px; }

.repo-row {
  display: grid;
  grid-template-columns: 18px 160px 1fr auto;
  gap: 10px;
  align-items: center;
  padding: 6px 8px;
  border-radius: 6px;
  transition: background 0.1s ease;
}
.repo-row:hover { background: #f8fafc; }
.repo-row.is-empty { opacity: 0.85; }

.repo-row-status {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px; height: 18px;
  border-radius: 9px;
  font-size: 11px;
  line-height: 1;
  font-weight: 700;
}
.repo-row-status.saved { background: #dcfce7; color: #15803d; }
.repo-row-status.edited { background: #fef3c7; color: #b45309; }
.repo-row-status.empty { background: #f1f5f9; color: #cbd5e1; }

.repo-row-name {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12.5px;
  color: var(--c-ink, #1e293b);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-weight: 500;
}
.repo-row-path {
  flex: 1; min-width: 0; width: 100%;
  padding: 6px 10px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  color: var(--c-ink, #475569);
  background: #f8fafc;
  border: 1px solid var(--c-border, #e2e8f0);
  border-radius: 5px;
  outline: none;
}
.repo-row-path:focus { border-color: #93c5fd; background: #fff; }
.repo-row-path::placeholder { color: #94a3b8; font-style: italic; }
.repo-row.is-empty .repo-row-path { background: #fafafa; }

.repo-row-actions {
  display: inline-flex; align-items: center; gap: 4px;
  flex-shrink: 0;
}
.icon-btn {
  width: 30px; height: 30px;
  display: inline-flex; align-items: center; justify-content: center;
  font-size: 14px;
  background: #fff;
  border: 1px solid var(--c-border, #e2e8f0);
  border-radius: 5px;
  cursor: pointer;
  transition: all 0.1s ease;
  color: var(--c-ink, #475569);
  padding: 0;
}
.icon-btn:hover:not(:disabled) {
  background: #eff6ff; border-color: #93c5fd; color: #1d4ed8;
}
.icon-btn:disabled { opacity: 0.4; cursor: not-allowed; }
.icon-btn-danger:hover:not(:disabled) {
  background: #fef2f2; border-color: #fca5a5; color: #b91c1c;
}
.icon-btn-placeholder { display: inline-block; width: 30px; height: 30px; }

.repo-paths-footer {
  margin-top: 10px;
  padding-top: 10px;
  border-top: 1px dashed var(--c-border, #e2e8f0);
  display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
}
.repo-paths-footer-hint {
  font-size: 11.5px; color: var(--c-muted, #64748b);
  flex: 1; min-width: 200px;
  line-height: 1.4;
}
.auto-clone-toggle {
  display: inline-flex; align-items: center; gap: 6px;
  font-size: 12.5px; color: var(--c-ink, #475569);
  cursor: pointer;
  padding: 4px 10px;
  border-radius: 5px;
  background: #f8fafc;
  border: 1px solid var(--c-border, #e2e8f0);
}
.auto-clone-toggle:hover { background: #eff6ff; border-color: #93c5fd; }
.auto-clone-toggle input { margin: 0; }
.auto-clone-dest {
  font-family: ui-monospace, monospace;
  font-size: 11px;
  color: var(--c-muted, #64748b);
  margin-left: 4px;
  padding: 1px 6px;
  background: #fff;
  border-radius: 3px;
  border: 1px solid var(--c-border, #e2e8f0);
}
</style>
