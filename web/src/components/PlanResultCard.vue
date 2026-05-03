<script setup lang="ts">
// PlanResultCard —— EditorPage "📋 生成计划" 按钮的结果展示。
// 自带文件目录树构建(create/modify/remove/preserved 4 桶合并 + 嵌套 dir 折叠 + 计数汇总)。
// 父端只透 plan 结果对象;tree 状态(折叠集合)在本组件内部管理。

import { computed, ref } from 'vue'

type PlanFileStatus = 'create' | 'modify' | 'remove' | 'preserved'
interface PlanTreeNode {
  kind: 'dir' | 'file'
  name: string
  path: string
  depth: number
  status?: PlanFileStatus
  counts?: { create: number, modify: number, remove: number, preserved: number }
}

interface PlanData {
  system?: string
  skills_included?: { name: string, reason?: string }[]
  skills_skipped?: { name: string, reason?: string }[]
  files_create?: string[]
  files_modify?: string[]
  files_remove?: string[]
  preserved?: string[]
  config_map_projection?: {
    verified_from_analyzer?: number
    verified_from_prior?: number
    inferred?: number
    total?: number
  }
}

const props = defineProps<{
  data: PlanData
}>()

const collapsed = ref<Set<string>>(new Set())

// 把 4 桶 (create/modify/remove/preserved) 合并成嵌套 tree → DFS 摊平 → 算每个 dir 的子级计数
const flatTree = computed<PlanTreeNode[]>(() => {
  const buckets: { paths: string[] | undefined, status: PlanFileStatus }[] = [
    { paths: props.data.files_create, status: 'create' },
    { paths: props.data.files_modify, status: 'modify' },
    { paths: props.data.files_remove, status: 'remove' },
    { paths: props.data.preserved, status: 'preserved' },
  ]
  // 同路径多次出现时按优先级合并(create > modify > remove > preserved)
  const priority: Record<PlanFileStatus, number> = {
    create: 4, modify: 3, remove: 2, preserved: 1,
  }
  const fileMap = new Map<string, PlanFileStatus>()
  for (const b of buckets) {
    if (!b.paths) continue
    for (const p of b.paths) {
      const cur = fileMap.get(p)
      if (!cur || priority[b.status] > priority[cur]) fileMap.set(p, b.status)
    }
  }
  type RawNode = { dirs: Map<string, RawNode>, files: { name: string, status: PlanFileStatus }[] }
  const root: RawNode = { dirs: new Map(), files: [] }
  for (const [path, status] of fileMap) {
    const parts = path.split('/')
    let cur = root
    for (let i = 0; i < parts.length - 1; i++) {
      const seg = parts[i]
      let child = cur.dirs.get(seg)
      if (!child) {
        child = { dirs: new Map(), files: [] }
        cur.dirs.set(seg, child)
      }
      cur = child
    }
    cur.files.push({ name: parts[parts.length - 1], status })
  }
  const out: PlanTreeNode[] = []
  function walk(node: RawNode, prefix: string, depth: number): { create: number, modify: number, remove: number, preserved: number } {
    const total = { create: 0, modify: 0, remove: 0, preserved: 0 }
    const sortedDirs = [...node.dirs.keys()].sort()
    for (const dirName of sortedDirs) {
      const dirPath = prefix ? `${prefix}/${dirName}` : dirName
      const idx = out.length
      out.push({ kind: 'dir', name: dirName, path: dirPath, depth, counts: { create: 0, modify: 0, remove: 0, preserved: 0 } })
      const sub = walk(node.dirs.get(dirName)!, dirPath, depth + 1)
      out[idx].counts = sub
      total.create += sub.create
      total.modify += sub.modify
      total.remove += sub.remove
      total.preserved += sub.preserved
    }
    const sortedFiles = node.files.slice().sort((a, b) => a.name.localeCompare(b.name))
    for (const f of sortedFiles) {
      const fullPath = prefix ? `${prefix}/${f.name}` : f.name
      out.push({ kind: 'file', name: f.name, path: fullPath, depth, status: f.status })
      total[f.status]++
    }
    return total
  }
  walk(root, '', 0)
  return out
})

// 折叠态过滤:任一祖先目录被折叠就过滤掉
const visibleNodes = computed<PlanTreeNode[]>(() => {
  const all = flatTree.value
  const c = collapsed.value
  if (c.size === 0) return all
  return all.filter(n => {
    const parts = n.path.split('/')
    for (let i = 1; i < parts.length; i++) {
      const ancestor = parts.slice(0, i).join('/')
      if (c.has(ancestor)) return false
    }
    return true
  })
})

const totalFiles = computed<number>(() => {
  const r = props.data
  return (r.files_create?.length || 0) + (r.files_modify?.length || 0)
    + (r.files_remove?.length || 0) + (r.preserved?.length || 0)
})

function toggleDir(path: string) {
  const next = new Set(collapsed.value)
  if (next.has(path)) next.delete(path)
  else next.add(path)
  collapsed.value = next
}
function expandAll() { collapsed.value = new Set() }
function collapseAll() {
  const next = new Set<string>()
  for (const n of flatTree.value) {
    if (n.kind === 'dir') next.add(n.path)
  }
  collapsed.value = next
}
</script>

<template>
  <div class="result-card">
    <h2>生成计划:{{ data.system }}</h2>
    <div class="result-grid">
      <div class="result-section">
        <h3>会启用的技能 ({{ data.skills_included?.length || 0 }})</h3>
        <ul v-if="data.skills_included?.length">
          <li v-for="s in data.skills_included" :key="s.name">
            <strong>{{ s.name }}</strong>
            <span v-if="s.reason" class="sub-text"> — {{ s.reason }}</span>
          </li>
        </ul>
        <p v-else class="sub-text">无</p>
      </div>
      <div class="result-section">
        <h3>会跳过的技能 ({{ data.skills_skipped?.length || 0 }})</h3>
        <ul v-if="data.skills_skipped?.length">
          <li v-for="s in data.skills_skipped" :key="s.name">
            <strong>{{ s.name }}</strong>
            <span v-if="s.reason" class="sub-text"> — {{ s.reason }}</span>
          </li>
        </ul>
        <p v-else class="sub-text">无</p>
      </div>
    </div>
    <div class="result-grid">
      <div class="result-section">
        <h3>会产出的文件</h3>
        <p><span class="badge badge-green">新建 {{ data.files_create?.length || 0 }}</span></p>
        <p><span class="badge badge-blue">改动 {{ data.files_modify?.length || 0 }}</span></p>
        <p><span class="badge badge-red">删除 {{ data.files_remove?.length || 0 }}</span></p>
        <p><span class="badge badge-gray">保留 {{ data.preserved?.length || 0 }}</span></p>
      </div>
      <div class="result-section">
        <h3>配置中心映射条数</h3>
        <table class="mini-table">
          <tr><td>仓库扫描得到</td><td>{{ data.config_map_projection?.verified_from_analyzer ?? 0 }}</td></tr>
          <tr><td>用户手填</td><td>{{ data.config_map_projection?.verified_from_prior ?? 0 }}</td></tr>
          <tr><td>规则推断</td><td>{{ data.config_map_projection?.inferred ?? 0 }}</td></tr>
          <tr><td><strong>总计</strong></td><td><strong>{{ data.config_map_projection?.total ?? 0 }}</strong></td></tr>
        </table>
      </div>
    </div>

    <div v-if="totalFiles > 0" class="result-section result-section-full">
      <div class="plan-tree-head">
        <h3>文件目录树 ({{ totalFiles }} 个文件)</h3>
        <div class="plan-tree-actions">
          <button type="button" class="btn-link" @click="expandAll">全部展开</button>
          <span class="sep">·</span>
          <button type="button" class="btn-link" @click="collapseAll">全部折叠</button>
        </div>
      </div>
      <div class="plan-tree">
        <div
          v-for="n in visibleNodes"
          :key="n.kind + ':' + n.path"
          class="plan-tree-row"
          :class="[
            'plan-tree-' + n.kind,
            n.kind === 'file' && n.status ? 'plan-tree-status-' + n.status : '',
          ]"
          :style="{ paddingLeft: (8 + n.depth * 16) + 'px' }"
          :role="n.kind === 'dir' ? 'button' : undefined"
          @click="n.kind === 'dir' ? toggleDir(n.path) : null"
        >
          <span v-if="n.kind === 'dir'" class="plan-tree-toggle">
            {{ collapsed.has(n.path) ? '▶' : '▼' }}
          </span>
          <span v-else class="plan-tree-toggle plan-tree-toggle-spacer"></span>
          <span class="plan-tree-icon">{{ n.kind === 'dir' ? '📁' : '📄' }}</span>
          <span class="plan-tree-name">{{ n.name }}<span v-if="n.kind === 'dir'">/</span></span>
          <span class="plan-tree-meta">
            <template v-if="n.kind === 'dir' && n.counts">
              <span v-if="n.counts.create" class="badge badge-green">+{{ n.counts.create }}</span>
              <span v-if="n.counts.modify" class="badge badge-blue">~{{ n.counts.modify }}</span>
              <span v-if="n.counts.remove" class="badge badge-red">−{{ n.counts.remove }}</span>
              <span v-if="n.counts.preserved" class="badge badge-gray">{{ n.counts.preserved }}</span>
            </template>
            <template v-else-if="n.kind === 'file'">
              <span v-if="n.status === 'create'" class="badge badge-green">新建</span>
              <span v-else-if="n.status === 'modify'" class="badge badge-blue">改动</span>
              <span v-else-if="n.status === 'remove'" class="badge badge-red">删除</span>
              <span v-else-if="n.status === 'preserved'" class="badge badge-gray">保留</span>
            </template>
          </span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.result-card {
  margin-top: 20px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  padding: 20px 24px;
}
.result-card h2 {
  font-size: 18px;
  color: #1e293b;
  margin-bottom: 16px;
  padding-bottom: 8px;
  border-bottom: 1px solid #e2e8f0;
}
.result-card h3 {
  font-size: 14px;
  color: #475569;
  margin-bottom: 8px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.result-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 20px;
  margin-bottom: 16px;
}
.result-section ul { list-style: none; padding: 0; }
.result-section li {
  padding: 4px 0;
  font-size: 13px;
  color: #334155;
}
.result-section-full { margin-top: 16px; }

.mini-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.mini-table td {
  padding: 6px 12px;
  border-bottom: 1px solid #e2e8f0;
  color: #334155;
}
.mini-table tr:last-child td { border-bottom: none; }
.mini-table td:first-child { color: #64748b; width: 180px; }

.plan-tree-head {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: 8px;
}
.plan-tree-head h3 { margin-bottom: 0; }
.plan-tree-actions { display: flex; align-items: center; gap: 6px; font-size: 12px; }
.plan-tree-actions .sep { color: #cbd5e1; }

.plan-tree {
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  background: #f8fafc;
  max-height: 480px;
  overflow-y: auto;
  padding: 4px 0;
  font-size: 12.5px;
}
.plan-tree-row {
  display: flex; align-items: center; gap: 6px;
  padding: 3px 12px 3px 8px;
  user-select: none;
  white-space: nowrap;
  font-family: 'SF Mono', Menlo, Consolas, monospace;
}
.plan-tree-dir { cursor: pointer; color: #1e293b; font-weight: 500; }
.plan-tree-dir:hover { background: #e2e8f0; }
.plan-tree-file { color: #334155; }
.plan-tree-file:hover { background: #eef2f7; }
.plan-tree-toggle {
  flex-shrink: 0; width: 12px; text-align: center;
  font-size: 9px; color: #64748b;
}
.plan-tree-toggle-spacer { visibility: hidden; }
.plan-tree-icon { flex-shrink: 0; font-size: 12px; }
.plan-tree-name {
  flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis;
}
.plan-tree-meta {
  flex-shrink: 0; display: flex; align-items: center; gap: 4px;
}
.plan-tree-meta :deep(.badge) { padding: 1px 6px; font-size: 10.5px; }
.plan-tree-status-remove .plan-tree-name { color: #991b1b; text-decoration: line-through; opacity: 0.7; }
.plan-tree-status-preserved .plan-tree-name { color: #64748b; font-style: italic; }
</style>
