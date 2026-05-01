<script setup lang="ts">
// WorkspaceBrowser —— 已装机器人工作目录浏览器(modal).
//
// BotsPage 点"📂 浏览工作目录"打开,左侧文件树 / 右侧编辑器(纯 textarea,Monaco 太重).
// 用户改 SKILL.md / scripts / 配置等任意文本文件直接写盘 —— 适合"调试一个 skill 改个变量
// 但不想动 system.yaml / 重新部署"的快速迭代场景.
//
// 范围限制:rootPath 必须是 discover.Scan 出来的真实部署根(后端 binding 强制校验);
// generator 管理的 tshoot.json / .clawhub/lock.json 后端拒写,UI 直接显示只读.
//
// 持久化未保存提示:editor 内容跟磁盘内容比对,不同时给红点 + 关闭前确认,避免误丢编辑.
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import {
  listBotWorkspaceFiles,
  readBotWorkspaceFile,
  writeBotWorkspaceFile,
  revealInFinder,
  isDesktop,
  type FileNode,
} from '../lib/bridge'
import { toast } from '../lib/toast'
import FileTreeNode from './FileTreeNode.vue'

const props = defineProps<{
  rootPath: string
  bot: { meta?: { system_id?: string; target?: string }; path: string }
}>()
const emit = defineEmits<{ (e: 'close'): void }>()

const tree = ref<FileNode | null>(null)
const treeError = ref('')
const loadingTree = ref(false)

const selectedPath = ref<string>('')      // 相对 rootPath 的 path,空 = 没选
const selectedAbs = computed(() => selectedPath.value
  ? `${props.rootPath.replace(/\/+$/, '')}/${selectedPath.value}`
  : '')

const fileLoading = ref(false)
const fileContent = ref<string>('')        // 当前 textarea 的值
const fileOriginal = ref<string>('')       // 上次从磁盘读到的原始内容,跟 fileContent 比对决定 dirty
const fileIsBinary = ref(false)
const fileTruncated = ref(false)
const fileSize = ref(0)
const fileError = ref('')
const saving = ref(false)

const isDirty = computed(() => !fileIsBinary.value && fileContent.value !== fileOriginal.value)
const isReadOnly = computed(() => {
  // generator 管理的元数据文件,后端会拒写;UI 提前标只读避免用户白改
  if (!selectedPath.value) return false
  const base = selectedPath.value.split('/').pop() || ''
  if (base === 'tshoot.json') return true
  if (selectedPath.value === '.clawhub/lock.json') return true
  return false
})

// hidden 文件折叠展示(.clawhub/.openclaw 这类元数据目录默认折叠减少视觉干扰)
const showHidden = ref(false)
const visibleTree = computed<FileNode | null>(() => {
  if (!tree.value) return null
  if (showHidden.value) return tree.value
  return filterHidden(tree.value)
})
function filterHidden(node: FileNode): FileNode {
  if (!node.children) return node
  return {
    ...node,
    children: node.children
      .filter(c => !c.name.startsWith('.'))
      .map(c => c.is_dir ? filterHidden(c) : c),
  }
}

async function loadTree() {
  loadingTree.value = true
  treeError.value = ''
  try {
    const t = await listBotWorkspaceFiles(props.rootPath)
    tree.value = t
  } catch (e: any) {
    treeError.value = String(e?.message || e)
    toast.error(`列文件树失败: ${treeError.value.slice(0, 80)}`)
  } finally {
    loadingTree.value = false
  }
}

async function pickFile(path: string) {
  // 切文件前如果当前有未保存改动 → 二次确认
  if (isDirty.value) {
    const ok = window.confirm(`当前文件 "${selectedPath.value}" 有未保存改动,切到别的文件会丢失。继续?`)
    if (!ok) return
  }
  selectedPath.value = path
  await loadFile()
}
async function loadFile() {
  if (!selectedPath.value) return
  fileLoading.value = true
  fileError.value = ''
  fileIsBinary.value = false
  fileTruncated.value = false
  try {
    const r = await readBotWorkspaceFile(props.rootPath, selectedPath.value)
    fileIsBinary.value = r.is_binary
    fileTruncated.value = !!r.truncated
    fileSize.value = r.size
    fileContent.value = r.content
    fileOriginal.value = r.content
  } catch (e: any) {
    fileError.value = String(e?.message || e)
  } finally {
    fileLoading.value = false
  }
}

async function save() {
  if (!selectedPath.value || isReadOnly.value) return
  saving.value = true
  try {
    await writeBotWorkspaceFile(props.rootPath, selectedPath.value, fileContent.value)
    fileOriginal.value = fileContent.value
    toast.success(`✓ 已保存 ${selectedPath.value}`)
  } catch (e: any) {
    toast.error(`保存失败: ${String(e?.message || e).slice(0, 100)}`)
  } finally {
    saving.value = false
  }
}

function revertChanges() {
  fileContent.value = fileOriginal.value
}

async function revealInOS() {
  if (!isDesktop()) return
  try {
    await revealInFinder(selectedAbs.value || props.rootPath)
  } catch { /* ignore */ }
}

function tryClose() {
  if (isDirty.value) {
    const ok = window.confirm(`当前文件 "${selectedPath.value}" 有未保存改动,关闭会丢失。继续?`)
    if (!ok) return
  }
  emit('close')
}

// Cmd/Ctrl+S 保存;Esc 关闭
function onKey(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key === 's') {
    e.preventDefault()
    if (isDirty.value && !isReadOnly.value) save()
  } else if (e.key === 'Escape') {
    tryClose()
  }
}

onMounted(() => {
  loadTree()
  window.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
watch(() => props.rootPath, loadTree)

function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
</script>

<template>
  <div class="ws-mask" @click.self="tryClose">
    <div class="ws-modal">
      <div class="ws-header">
        <div class="ws-title">
          <span class="ws-target-tag" :data-target="bot.meta?.target || ''">{{ bot.meta?.target || '?' }}</span>
          <strong>{{ bot.meta?.system_id || '机器人' }}</strong>
          <span class="ws-path muted" :title="rootPath">{{ rootPath }}</span>
        </div>
        <div class="ws-header-actions">
          <label class="ws-toggle" title="勾上后展示 . 开头的隐藏文件/目录(.clawhub / .openclaw 等元数据)">
            <input type="checkbox" v-model="showHidden" />
            显示隐藏文件
          </label>
          <button class="btn small" @click="loadTree" :disabled="loadingTree" title="重新读取目录树(磁盘文件外部改动后用这个刷新)">🔄 刷新树</button>
          <button class="btn small" @click="revealInOS" v-if="isDesktop()" title="在系统文件管理器(Finder)里打开">📂 在 Finder 中打开</button>
          <button class="btn-icon close" @click="tryClose" title="关闭(Esc)">×</button>
        </div>
      </div>

      <div class="ws-body">
        <!-- 左侧文件树 -->
        <div class="ws-tree">
          <div v-if="loadingTree" class="ws-loading">加载中…</div>
          <div v-else-if="treeError" class="ws-error">✗ {{ treeError }}</div>
          <template v-else-if="visibleTree && visibleTree.children?.length">
            <FileTreeNode
              v-for="child in visibleTree.children"
              :key="child.path"
              :node="child"
              :selected="selectedPath"
              :depth="0"
              @pick="pickFile"
            />
          </template>
          <div v-else class="ws-empty">空目录</div>
        </div>

        <!-- 右侧编辑器 -->
        <div class="ws-editor">
          <div v-if="!selectedPath" class="ws-placeholder">
            ← 从左侧点选一个文件查看内容<br/>
            <span class="muted">编辑后点保存或 Cmd/Ctrl+S 写盘。改 SKILL.md / 脚本变量等做调试用.</span>
          </div>
          <div v-else class="ws-editor-wrap">
            <div class="ws-editor-head">
              <span class="ws-file-name">{{ selectedPath }}</span>
              <span v-if="isDirty" class="ws-dirty-dot" title="有未保存改动">●</span>
              <span class="muted">{{ fmtSize(fileSize) }}</span>
              <span v-if="fileTruncated" class="tag orange">已截断显示前 1MB</span>
              <span v-if="fileIsBinary" class="tag gray">二进制</span>
              <span v-if="isReadOnly" class="tag gray" title="generator 管理,改了下次部署会被覆盖">只读</span>
              <div class="ws-editor-actions">
                <button class="btn small" :disabled="!isDirty" @click="revertChanges" title="撤销本次改动,回到磁盘里的版本">↺ 撤销</button>
                <button class="btn small primary" :disabled="!isDirty || isReadOnly || saving" @click="save" title="Cmd/Ctrl+S">
                  {{ saving ? '保存中…' : '💾 保存' }}
                </button>
              </div>
            </div>
            <div v-if="fileLoading" class="ws-loading">读取中…</div>
            <div v-else-if="fileError" class="ws-error">✗ {{ fileError }}</div>
            <div v-else-if="fileIsBinary" class="ws-binary">
              ⚠ 这是个二进制文件,UI 不支持编辑.<br/>
              如果要看内容请用「📂 在 Finder 中打开」选定后用对应工具.
            </div>
            <textarea
              v-else
              v-model="fileContent"
              class="ws-textarea"
              :readonly="isReadOnly"
              spellcheck="false"
              placeholder="(空文件)"
            />
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ws-mask {
  position: fixed; inset: 0; z-index: 1000;
  background: rgba(0,0,0,0.45);
  display: flex; align-items: center; justify-content: center;
}
.ws-modal {
  width: 92vw; height: 88vh; max-width: 1400px;
  background: #fff; border-radius: 8px;
  display: flex; flex-direction: column;
  box-shadow: 0 20px 60px rgba(0,0,0,0.3);
  overflow: hidden;
}

.ws-header {
  flex-shrink: 0;
  padding: 10px 14px;
  border-bottom: 1px solid #e2e8f0;
  display: flex; align-items: center; gap: 12px;
  background: #f8fafc;
}
.ws-title { display: flex; align-items: center; gap: 8px; flex: 1; min-width: 0; font-size: 13px; }
.ws-title strong { color: #0f172a; }
.ws-path { font-family: ui-monospace, monospace; font-size: 11px; color: #64748b; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
.ws-target-tag {
  font-size: 10px; padding: 1px 6px; border-radius: 3px; font-weight: 600;
  background: #e0e7ff; color: #3730a3;
}
.ws-target-tag[data-target="openclaw"] { background: #fce7f3; color: #9f1239; }
.ws-target-tag[data-target="claude-code"] { background: #fef3c7; color: #92400e; }
.ws-target-tag[data-target="cursor"] { background: #dbeafe; color: #1e40af; }
.ws-target-tag[data-target="codex"] { background: #d1fae5; color: #065f46; }
.ws-header-actions { display: flex; align-items: center; gap: 8px; }
.ws-toggle { font-size: 11px; color: #475569; display: flex; align-items: center; gap: 4px; cursor: pointer; }
.btn-icon.close {
  width: 28px; height: 28px; border: none; background: transparent;
  font-size: 20px; line-height: 1; cursor: pointer; color: #64748b; border-radius: 4px;
}
.btn-icon.close:hover { background: #e2e8f0; color: #0f172a; }

.ws-body {
  flex: 1; min-height: 0;
  display: grid; grid-template-columns: 280px 1fr;
}

.ws-tree {
  border-right: 1px solid #e2e8f0;
  overflow-y: auto;
  background: #fafbfc;
  padding: 6px 0;
}
.ws-loading, .ws-empty, .ws-placeholder, .ws-error, .ws-binary {
  padding: 24px;
  font-size: 12px; color: #64748b;
  text-align: center;
}
.ws-error { color: #b91c1c; }
.ws-placeholder { line-height: 1.6; }

.ws-editor {
  display: flex; flex-direction: column;
  min-width: 0;
}
.ws-editor-wrap { display: flex; flex-direction: column; flex: 1; min-height: 0; }
.ws-editor-head {
  flex-shrink: 0;
  padding: 8px 14px;
  border-bottom: 1px solid #e2e8f0;
  display: flex; align-items: center; gap: 10px;
  font-size: 12px;
  background: #f8fafc;
}
.ws-file-name { font-family: ui-monospace, monospace; color: #1e293b; }
.ws-dirty-dot { color: #ef4444; font-size: 12px; }
.muted { color: #64748b; font-size: 11px; }
.ws-editor-actions { margin-left: auto; display: flex; gap: 6px; }

.ws-textarea {
  flex: 1; min-height: 0;
  border: none; outline: none;
  padding: 12px 16px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 13px; line-height: 1.55;
  color: #1e293b;
  resize: none;
  background: #fff;
  white-space: pre;
  overflow: auto;
}
.ws-textarea:read-only { background: #f8fafc; color: #475569; }

.tag.orange { background: #fed7aa; color: #9a3412; padding: 1px 6px; border-radius: 3px; font-size: 10px; }
.tag.gray { background: #e5e7eb; color: #374151; padding: 1px 6px; border-radius: 3px; font-size: 10px; }
</style>
