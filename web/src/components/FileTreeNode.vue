<script setup lang="ts">
// FileTreeNode —— WorkspaceBrowser 的递归子组件。拆出来是为了 Vue SFC 自身递归引用
// (component template 里 <FileTreeNode> 引自己),defineOptions({name}) 让它能注册自身名。
import { ref } from 'vue'
import type { FileNode } from '../lib/bridge'

defineOptions({ name: 'FileTreeNode' })

const props = defineProps<{
  node: FileNode
  selected: string
  depth?: number
}>()
const emit = defineEmits<{ (e: 'pick', path: string): void }>()

// 浅层(depth<2)默认展开,深层折叠(避免一打开就铺一屏)
const expanded = ref((props.depth ?? 0) < 2)

function toggle() {
  if (props.node.is_dir) expanded.value = !expanded.value
  else emit('pick', props.node.path)
}
function pick(p: string) { emit('pick', p) }

function fileIcon(node: FileNode): string {
  if (node.is_dir) return expanded.value ? '📂' : '📁'
  const lower = node.name.toLowerCase()
  if (lower.endsWith('.md')) return '📝'
  if (lower.endsWith('.yaml') || lower.endsWith('.yml')) return '⚙️'
  if (lower.endsWith('.json')) return '🗂️'
  if (lower.endsWith('.sh') || lower.endsWith('.py') || lower.endsWith('.js') || lower.endsWith('.ts')) return '🛠️'
  return '📄'
}
</script>

<template>
  <div class="tree-node">
    <div
      class="tree-row"
      :class="{ 'is-dir': node.is_dir, selected: !node.is_dir && selected === node.path }"
      :style="{ paddingLeft: ((depth ?? 0) * 14 + 8) + 'px' }"
      @click="toggle"
    >
      <span class="tree-icon">{{ fileIcon(node) }}</span>
      <span class="tree-name">{{ node.name }}</span>
    </div>
    <div v-if="node.is_dir && expanded && node.children?.length">
      <FileTreeNode
        v-for="c in node.children"
        :key="c.path"
        :node="c"
        :selected="selected"
        :depth="(depth ?? 0) + 1"
        @pick="pick"
      />
    </div>
  </div>
</template>

<style scoped>
.tree-row {
  display: flex; align-items: center; gap: 6px;
  padding: 4px 8px;
  cursor: pointer;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-size: 12px;
  color: #1e293b;
}
.tree-row:hover { background: #e0e7ff; }
.tree-row.selected { background: #3b82f6; color: #fff; }
.tree-row.selected .tree-icon { filter: grayscale(1) brightness(2); }
.tree-row.is-dir { font-weight: 500; }
.tree-icon { flex-shrink: 0; font-size: 13px; }
.tree-name { overflow: hidden; text-overflow: ellipsis; }
</style>
