<script setup lang="ts">
// PreviewResultCard —— EditorPage "📂 预览产物" 按钮的结果展示。
// 左侧文件树(按目录分组)+ 右侧文件内容预览,真跑 generator 到 tmp 目录后的快照。

import { computed, ref, watch } from 'vue'
import type { GenPreviewFile, GenPreviewResult } from '../lib/bridge'

const props = defineProps<{
  result: GenPreviewResult
}>()

const activePath = ref<string>('')

// 默认选中"看着像入口的"文件;result 切换时重选
watch(() => props.result, (r) => {
  const firstHit =
    r.files.find(f => f.path.endsWith('SOUL.md')) ||
    r.files.find(f => f.path.endsWith('tshoot.json')) ||
    r.files.find(f => /\bSKILL\.md$/.test(f.path)) ||
    r.files[0]
  activePath.value = firstHit?.path || ''
}, { immediate: true })

// 按"完整目录路径"分组(如 skills/routing/SKILL.md → 组 "skills/routing")。
// 两段以下视为根组 "/",避免无意义的组层级。
const groups = computed<{ dir: string, files: GenPreviewFile[] }[]>(() => {
  const map: Record<string, GenPreviewFile[]> = {}
  for (const f of props.result.files) {
    const parts = f.path.split('/')
    const dir = parts.length > 1 ? parts.slice(0, -1).join('/') : '/'
    if (!map[dir]) map[dir] = []
    map[dir].push(f)
  }
  return Object.keys(map).sort().map(dir => ({ dir, files: map[dir] }))
})

const activeFile = computed<GenPreviewFile | null>(() => {
  return props.result.files.find(f => f.path === activePath.value) || null
})

function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
</script>

<template>
  <div class="result-card preview-card">
    <h2>
      📂 产物预览:{{ result.system }}
      <span class="sub-text" style="font-weight:normal">
        · 部署目标:{{ result.targets.join(' / ') }}
        · 共 {{ result.files.length }} 个文件
      </span>
    </h2>
    <div class="preview-layout">
      <div class="preview-tree">
        <div v-for="g in groups" :key="g.dir" class="preview-group">
          <div class="preview-group-head">{{ g.dir }}</div>
          <button
            v-for="f in g.files"
            :key="f.path"
            class="preview-file"
            :class="{ active: f.path === activePath, binary: f.binary }"
            :title="`${f.path} (${fmtSize(f.size)}${f.binary ? ', 二进制' : ''}${f.truncated ? ', 已截断' : ''})`"
            @click="activePath = f.path"
          >
            <span class="preview-file-name">{{ f.path.split('/').pop() }}</span>
            <span class="preview-file-meta">{{ fmtSize(f.size) }}<span v-if="f.binary"> · bin</span></span>
          </button>
        </div>
      </div>
      <div class="preview-content">
        <template v-if="activeFile">
          <div class="preview-content-head">
            <code>{{ activeFile.path }}</code>
            <span class="sub-text">{{ fmtSize(activeFile.size) }}</span>
            <span v-if="activeFile.truncated" class="badge badge-orange">已截断(头部 200KB)</span>
            <span v-if="activeFile.binary" class="badge badge-gray">二进制文件</span>
          </div>
          <pre v-if="activeFile.binary" class="preview-content-body muted">[二进制文件,无法预览]</pre>
          <pre v-else class="preview-content-body"><code>{{ activeFile.content }}</code></pre>
        </template>
        <p v-else class="sub-text" style="padding:20px">点左侧文件查看内容</p>
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
  padding: 0;
  overflow: hidden;
}
.preview-card h2 { padding: 14px 16px; margin: 0; border-bottom: 1px solid #e2e8f0; font-size: 15px; color: #1e293b; }
.preview-layout {
  display: grid;
  grid-template-columns: minmax(240px, 320px) 1fr;
  height: clamp(480px, calc(100vh - 320px), 720px);
  min-height: 0;
}
.preview-tree {
  border-right: 1px solid #e2e8f0;
  background: #f8fafc;
  overflow-y: auto; overflow-x: hidden;
  padding: 6px 0 24px;
  min-height: 0;
}
.preview-group { margin-bottom: 6px; }
.preview-group-head {
  font-size: 11px; font-family: monospace; color: #64748b;
  padding: 5px 12px 5px 14px; background: #eef2f7;
  border-bottom: 1px solid #e2e8f0;
  position: sticky; top: 0; z-index: 1;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.preview-file {
  display: flex; justify-content: space-between; align-items: center;
  width: 100%; padding: 6px 14px; gap: 8px;
  border: none; background: transparent; text-align: left; cursor: pointer;
  font-family: inherit; font-size: 12px;
}
.preview-file:hover { background: #e2e8f0; }
.preview-file.active { background: #dbeafe; color: #1e3a8a; font-weight: 600; }
.preview-file.binary { color: #94a3b8; }
.preview-file-name { font-family: monospace; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.preview-file-meta { font-size: 10px; color: #94a3b8; flex-shrink: 0; }
.preview-tree::-webkit-scrollbar { width: 10px; height: 10px; }
.preview-tree::-webkit-scrollbar-track { background: #f1f5f9; }
.preview-tree::-webkit-scrollbar-thumb { background: #cbd5e1; border-radius: 5px; border: 2px solid #f1f5f9; }
.preview-tree::-webkit-scrollbar-thumb:hover { background: #94a3b8; }

.preview-content { display: flex; flex-direction: column; min-width: 0; min-height: 0; }
.preview-content-head {
  display: flex; align-items: center; gap: 10px;
  padding: 10px 14px; border-bottom: 1px solid #e2e8f0;
  background: #fff;
}
.preview-content-head code {
  font-family: monospace; font-size: 12px; color: #1e293b;
  background: #f1f5f9; padding: 2px 6px; border-radius: 3px;
}
/* IDE 暗底:把全局 code{background:var(--c-surf-3)} 重置掉,避免每行 code span 撕裂 */
.preview-content-body {
  flex: 1 1 0; margin: 0; padding: 14px 16px 24px;
  background: #0d1117;
  color: #c9d1d9;
  font-family: 'SF Mono', 'Menlo', 'Consolas', monospace;
  font-size: 12.5px; line-height: 1.55;
  overflow: auto;
  min-height: 0;
  white-space: pre; tab-size: 2;
}
.preview-content-body code {
  background: transparent;
  padding: 0;
  border-radius: 0;
  color: inherit;
  font-family: inherit;
  font-size: inherit;
}
.preview-content-body.muted { color: #94a3b8; background: #f8fafc; font-style: italic; }
.preview-content-body.muted code { color: inherit; }
.preview-content-body::-webkit-scrollbar { width: 10px; height: 10px; }
.preview-content-body::-webkit-scrollbar-track { background: #161b22; }
.preview-content-body::-webkit-scrollbar-thumb { background: #30363d; border-radius: 5px; }
.preview-content-body::-webkit-scrollbar-thumb:hover { background: #484f58; }
</style>
