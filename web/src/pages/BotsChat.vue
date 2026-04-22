<script setup lang="ts">
// 桌面端原生 chat:Studio 自己用 OpenAI 兼容协议跟多 provider(Anthropic/OpenAI/
// DeepSeek/Qwen/MiniMax/Moonshot/智谱/Ollama)流式对话,无 Flask、无 iframe、
// 无 localhost 端口。token delta 通过 Wails event 回流,UI 跟其他页面同一壳。
//
// 状态机:
//   init       → 页面刚进,异步请求 ChatContextFor + 看会话 key 是否已填
//   need-key   → 第一次进来且没 env LLM_API_KEY,等用户填
//   ready      → 已准备好,可以发消息(messages 空 = 欢迎气泡)
//   streaming  → 正在收 token,btn 变"停止"
//
// 历史持久化:localStorage 按 bot path 分,跟 server.py / index.html 的策略一致,
// 防止切换机器人串词。env 选择也本地存。
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { EventsOff, EventsOn } from '../../wailsjs/runtime/runtime'
import type { ChatContext, ChatMessage } from '../lib/bridge'
import {
  chatCheckKey,
  chatContextFor,
  chatDeleteKey,
  chatLoadKey,
  chatSaveKey,
  chatSend,
  chatStop,
  isDesktop,
  revealInFinder,
} from '../lib/bridge'
import { confirmDialog } from '../lib/confirm'
import { toast } from '../lib/toast'
import { marked } from 'marked'

const route = useRoute()
const router = useRouter()

const botPath = computed(() => String(route.query.path || ''))
const botName = computed(() => String(route.query.name || '排障机器人'))

type Stage = 'init' | 'need-key' | 'ready' | 'streaming' | 'error'
const stage = ref<Stage>('init')
const chatCtx = ref<ChatContext | null>(null)
const apiKeyInput = ref('')
const rememberKey = ref(true)
const errMsg = ref<string | null>(null)

// key placeholder 按 provider 定制,给用户"该拿哪家 key"的强信号
const keyPlaceholder = computed(() => {
  switch (chatCtx.value?.provider_id) {
    case 'anthropic': return 'sk-ant-api03-...'
    case 'openai':    return 'sk-...'
    case 'deepseek':  return 'sk-... (DeepSeek)'
    case 'qwen':      return 'sk-... (DashScope)'
    case 'minimax':   return 'eyJ... (MiniMax JWT)'
    case 'moonshot':  return 'sk-... (Moonshot)'
    case 'zhipu':     return '智谱 API Key'
    case 'ollama':    return '(本地 Ollama 不需要 key,随便填)'
    default:          return '对应 provider 的 API key'
  }
})

const messages = ref<ChatMessage[]>([])
const inputText = ref('')
const defaultEnv = ref('')
const currentReqID = ref<string | null>(null) // streaming 期间非空
const messagesBox = ref<HTMLDivElement | null>(null)

// key 存储策略(优先级从高到低):
//   1. window 级 session store(__tshootChatKeys__):当前进程里已读出来的内存缓存
//      —— 发消息/预检时能直接拿,不用每次都去打扰系统钥匙串(钥匙串每次读都可能弹
//      macOS 的"允许"对话框,体验差)
//   2. 系统钥匙串(chatLoadKey):跨 app 重启持久,第一次打开 chat 页从这儿读回来
//      填进 session store。用户勾"保存"的话,submitKey 会写回这儿。
// sessionKeyStore 只是优化层,权威数据源是钥匙串。
const sessionKeyStore = ((window as any).__tshootChatKeys__ ??= {}) as Record<string, string>
const keySavedInKeychain = ref(false) // 当前 bot 的 key 是否从钥匙串读出来的;true 时 header 显示"重置 API key"

function storageKey(kind: 'msgs' | 'env'): string {
  return `ts-native-chat:${kind}:${botPath.value}`
}

function saveMessages() {
  try { localStorage.setItem(storageKey('msgs'), JSON.stringify(messages.value)) } catch { /* quota full; ignore */ }
}
function loadMessages(): ChatMessage[] {
  try {
    const raw = localStorage.getItem(storageKey('msgs'))
    return raw ? JSON.parse(raw) : []
  } catch { return [] }
}
function saveEnv(v: string) {
  try { v ? localStorage.setItem(storageKey('env'), v) : localStorage.removeItem(storageKey('env')) } catch { /* ignore */ }
}
function loadEnv(): string {
  try { return localStorage.getItem(storageKey('env')) || '' } catch { return '' }
}

async function init() {
  if (!isDesktop()) {
    errMsg.value = '原生 chat 只在桌面 app 可用'
    stage.value = 'error'
    return
  }
  if (!botPath.value) {
    toast.error('缺 path 参数')
    router.replace('/bots')
    return
  }
  try {
    chatCtx.value = await chatContextFor(botPath.value)
  } catch (e: any) {
    errMsg.value = String(e?.message || e)
    stage.value = 'error'
    return
  }
  messages.value = loadMessages()
  const savedEnv = loadEnv()
  // savedEnv 如果上次存的但 system.yaml 已经删掉那个 env 就清掉
  if (savedEnv && chatCtx.value!.envs.includes(savedEnv)) {
    defaultEnv.value = savedEnv
  } else {
    saveEnv('')
  }

  // key 查找顺序:session store(本次进程缓存) > 系统钥匙串(持久)
  if (sessionKeyStore[botPath.value]) {
    stage.value = 'ready'
    return
  }
  try {
    const r = await chatLoadKey(botPath.value)
    if (r.ok && r.api_key) {
      sessionKeyStore[botPath.value] = r.api_key
      keySavedInKeychain.value = true
      stage.value = 'ready'
      return
    }
  } catch {
    // keychain 读失败(比如 Linux 没 libsecret):静默,fallback 到 need-key,
    // 不打扰用户。用户填完 key 时如果 keychain 写入也失败,会给 toast 明说。
  }
  stage.value = 'need-key'
}

const keyChecking = ref(false)

async function submitKey() {
  const k = apiKeyInput.value.trim()
  if (!k) { errMsg.value = 'API key 不能空'; return }
  // 预检:先向 provider 发最小请求(max_tokens=1)验证 key 能用,避免用户填完
  // 发消息才收到 401 这种"先填半天再翻车"体验。ollama 本地之类不需要 key
  // 的场景预检会直接放行(CheckKey 里对空 key 会 reject,但 ollama 用户填
  // 任意字符串都能过它家 /chat/completions 的 auth)。
  keyChecking.value = true
  errMsg.value = null
  try {
    await chatCheckKey(botPath.value, k)
  } catch (e: any) {
    errMsg.value = `key 预检失败:${String(e?.message || e)}`
    keyChecking.value = false
    return
  }
  keyChecking.value = false
  sessionKeyStore[botPath.value] = k
  if (rememberKey.value) {
    try {
      await chatSaveKey(botPath.value, k)
      keySavedInKeychain.value = true
    } catch (e: any) {
      // keychain 写入失败(Linux 没 libsecret / macOS 用户拒了访问对话框)
      // fallback 到会话级,明确告诉用户为什么
      toast.error(`保存到系统钥匙串失败:${String(e?.message || e)}。key 仅本会话有效。`)
    }
  }
  stage.value = 'ready'
  apiKeyInput.value = ''
}

async function resetApiKey() {
  const ok = await confirmDialog({
    title: '重置 API key',
    message: '从系统钥匙串和当前会话里删除这台机器人的 API key。下次打开对话需要重新填。',
    confirmText: '重置',
    danger: true,
  })
  if (!ok) return
  delete sessionKeyStore[botPath.value]
  try {
    await chatDeleteKey(botPath.value)
  } catch { /* 本来就没存过 / 访问不到钥匙串,无所谓 */ }
  keySavedInKeychain.value = false
  toast.success('已重置 API key,下次打开对话会重新询问')
  stage.value = 'need-key'
}

function currentKey(): string {
  return sessionKeyStore[botPath.value] || (window as any).__tshootChatCurrentKey || ''
}

// 流式状态里的临时累积
const streamBuf = ref('')

async function send() {
  const text = inputText.value.trim()
  if (!text) return
  if (stage.value === 'streaming') return

  messages.value.push({ role: 'user', content: text })
  saveMessages()
  inputText.value = ''
  streamBuf.value = ''
  stage.value = 'streaming'
  await nextTick(scrollBottom)

  let reqID: string
  try {
    reqID = await chatSend({
      bot_path: botPath.value,
      api_key: currentKey(),
      messages: messages.value,
      default_env: defaultEnv.value,
    })
  } catch (e: any) {
    stage.value = 'ready'
    // 回退 messages(发送失败不留 user 半条),把输入框还给用户
    inputText.value = messages.value.pop()?.content || ''
    saveMessages()
    toast.error(`发送失败: ${String(e?.message || e)}`)
    return
  }
  currentReqID.value = reqID

  // 绑定本 reqID 专属的 3 个事件
  const deltaEv = `chat:delta:${reqID}`
  const doneEv = `chat:done:${reqID}`
  const errorEv = `chat:error:${reqID}`
  const cleanup = () => {
    EventsOff(deltaEv)
    EventsOff(doneEv)
    EventsOff(errorEv)
  }
  EventsOn(deltaEv, (d: string) => {
    streamBuf.value += d
    scrollBottom()
  })
  EventsOn(doneEv, () => {
    // 流结束:把积累的正文转正,推入历史,清临时
    if (streamBuf.value) {
      messages.value.push({ role: 'assistant', content: streamBuf.value })
      saveMessages()
    }
    streamBuf.value = ''
    currentReqID.value = null
    stage.value = 'ready'
    cleanup()
  })
  EventsOn(errorEv, (msg: string) => {
    // 错误(可能是 401/429 等):把错误文案也存一条,让用户能看到完整 context
    messages.value.push({ role: 'assistant', content: `❌ ${msg}` })
    saveMessages()
    streamBuf.value = ''
    currentReqID.value = null
    stage.value = 'ready'
    cleanup()
    toast.error(msg)
  })
}

async function stopStreaming() {
  if (!currentReqID.value) return
  await chatStop(currentReqID.value)
  // 等 done/error 事件回来自然走 cleanup;这里不清 currentReqID,避免 race
}

async function clearHistory() {
  if (messages.value.length === 0) return
  // 同 InitPage.clearDraft:Wails WebView 吞 window.confirm,改用自建 modal
  const ok = await confirmDialog({
    title: '清空对话',
    message: '清空当前对话历史?localStorage 里存的这个机器人的聊天记录会全部删除,不可恢复。',
    confirmText: '清空',
    danger: true,
  })
  if (!ok) return
  messages.value = []
  saveMessages()
}

function onEnvChange() {
  saveEnv(defaultEnv.value)
}

function scrollBottom() {
  nextTick(() => {
    if (messagesBox.value) {
      messagesBox.value.scrollTop = messagesBox.value.scrollHeight
    }
  })
}

// 最小 markdown 渲染(代码块 / 行内 / 粗体 / 链接 / 列表 / 标题);走 marked 库保质量
// 但不启用 raw html,避免 prompt injection 插标签。
marked.setOptions({ breaks: true, gfm: true })
function renderMd(s: string): string {
  // marked.parse 是同步的(这里传同步字符串),cast 即可
  return marked.parse(s, { async: false }) as string
}

onMounted(init)
onBeforeUnmount(() => {
  // 如果离开页面时还有流在跑,cancel 它,避免 token 继续烧
  if (currentReqID.value) {
    chatStop(currentReqID.value).catch(() => { /* ignore */ })
  }
})
</script>

<template>
  <div class="chat-page">
    <header class="chat-head">
      <button class="btn small" @click="router.push('/bots')">← 返回</button>
      <span class="chat-title">{{ botName }}</span>
      <span v-if="chatCtx" class="chat-model" :title="chatCtx.provider_name ? 'provider: ' + chatCtx.provider_name : 'provider 未识别'">
        {{ chatCtx.model }}{{ chatCtx.provider_id ? ' · ' + chatCtx.provider_id : '' }}
      </span>
      <span
        v-if="chatCtx && chatCtx.prompt_tokens > 0"
        class="prompt-size"
        :class="{ warn: chatCtx.prompt_tokens > 8000 }"
        :title="`system prompt ${chatCtx.prompt_chars} 字符 ≈ ${chatCtx.prompt_tokens} tokens。大部分模型 context window ≥ 32k,Moonshot v1-8k / MiniMax abab5.5 等小 context 模型超过 8k 可能截断`"
      >
        prompt ≈ {{ chatCtx.prompt_tokens > 1000 ? (chatCtx.prompt_tokens / 1000).toFixed(1) + 'k' : chatCtx.prompt_tokens }} tokens
        <span v-if="chatCtx.prompt_tokens > 8000"> ⚠</span>
      </span>
      <span v-if="chatCtx?.envs?.length" class="env-wrap">
        <span class="env-label">默认环境:</span>
        <select v-model="defaultEnv" class="env-sel" @change="onEnvChange">
          <option value="">未选择</option>
          <option v-for="e in chatCtx.envs" :key="e" :value="e">{{ e }}</option>
        </select>
      </span>
      <span class="chat-path" :title="botPath">📁 {{ botPath }}</span>
      <button v-if="stage === 'ready' || stage === 'streaming'" class="btn small" @click="revealInFinder(botPath)">
        在 Finder 显示
      </button>
      <button
        v-if="keySavedInKeychain && (stage === 'ready' || stage === 'streaming')"
        class="btn small btn-clear"
        :disabled="stage === 'streaming'"
        title="从系统钥匙串和当前会话里删除这台机器人的 API key"
        @click="resetApiKey"
      >
        🔑 重置 API key
      </button>
      <button v-if="stage === 'ready' || stage === 'streaming'" class="btn small btn-clear" :disabled="stage === 'streaming'" @click="clearHistory">
        清空对话
      </button>
    </header>

    <!-- 需要 key -->
    <div v-if="stage === 'need-key'" class="gate">
      <div class="gate-box">
        <h2>{{ botName }} 需要 API key</h2>
        <p class="gate-desc">
          当前模型 <code>{{ chatCtx?.model || '—' }}</code>
          <span v-if="chatCtx?.provider_name">(provider: <strong>{{ chatCtx.provider_name }}</strong>)</span>。
          请填入该 provider 的 API key。勾选"保存"后会加密存到系统钥匙串(macOS Keychain / Linux libsecret / Windows Credential Manager),下次打开对话自动读回来,不用再填。
        </p>
        <p v-if="!chatCtx?.provider_id" class="alert warn" style="font-size:12px;">
          ⚠ model 前缀未识别,可能发消息时报 "model 未识别" 错。要改 model 去创建向导或 yaml 调试器。
        </p>
        <input
          v-model="apiKeyInput"
          type="password"
          class="key-input"
          :placeholder="keyPlaceholder"
          @keyup.enter="submitKey"
        />
        <label class="remember">
          <input type="checkbox" v-model="rememberKey" />
          保存到系统钥匙串(加密,跨 app 重启持久;想清除用头部"重置 API key"按钮)
        </label>
        <div v-if="errMsg" class="alert error">{{ errMsg }}</div>
        <div class="gate-actions">
          <button class="btn" @click="router.push('/bots')">取消</button>
          <button class="btn primary" :disabled="!apiKeyInput.trim() || keyChecking" @click="submitKey">
            {{ keyChecking ? '验证 key…' : '开始对话' }}
          </button>
        </div>
      </div>
    </div>

    <!-- init 中(loading context) -->
    <div v-else-if="stage === 'init'" class="gate">
      <div class="gate-box">
        <div class="loading-anim">
          <div class="spinner"></div>
          <span>加载机器人上下文...</span>
        </div>
      </div>
    </div>

    <!-- 报错 -->
    <div v-else-if="stage === 'error'" class="gate">
      <div class="gate-box err">
        <h2>⚠ 无法初始化</h2>
        <pre class="err-detail">{{ errMsg }}</pre>
        <div class="gate-actions">
          <button class="btn" @click="router.push('/bots')">返回</button>
          <button class="btn primary" @click="init">重试</button>
        </div>
      </div>
    </div>

    <!-- 聊天主界面 -->
    <template v-else>
      <div ref="messagesBox" class="messages">
        <!-- 欢迎气泡:历史空时显示 -->
        <div v-if="messages.length === 0" class="msg bot welcome">
          你好,我是 <strong>{{ botName }}</strong>。请描述你遇到的问题,包括环境、服务名、错误现象。
          <div v-if="chatCtx?.envs?.length" class="welcome-hint">
            当前默认环境:<code>{{ defaultEnv || '未选择(提问时不会自动拼)' }}</code>
          </div>
        </div>
        <div
          v-for="(m, i) in messages"
          :key="i"
          class="msg"
          :class="m.role === 'user' ? 'user' : 'bot'"
        >
          <template v-if="m.role === 'user'">{{ m.content }}</template>
          <div v-else class="md" v-html="renderMd(m.content)" />
        </div>
        <!-- 流式期间的临时气泡,用 streamBuf 实时渲染 -->
        <div v-if="stage === 'streaming' && streamBuf" class="msg bot streaming">
          <div class="md" v-html="renderMd(streamBuf)" />
          <span class="cursor"></span>
        </div>
        <div v-else-if="stage === 'streaming'" class="msg bot streaming">
          <span class="cursor"></span>
        </div>
      </div>

      <div class="input-bar">
        <textarea
          v-model="inputText"
          class="input"
          placeholder="描述问题... (Shift+Enter 换行,Enter 发送)"
          :disabled="stage === 'streaming'"
          @keydown.enter.exact.prevent="send"
        />
        <button v-if="stage === 'streaming'" class="btn btn-stop" @click="stopStreaming">停止</button>
        <button v-else class="btn primary" :disabled="!inputText.trim()" @click="send">发送</button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.chat-page {
  display: flex; flex-direction: column; height: 100%;
  margin: -32px; background: #fff;
}
.chat-head {
  display: flex; align-items: center; gap: 10px;
  padding: 10px 14px; border-bottom: 1px solid var(--c-line);
  background: var(--c-surf-2); flex-shrink: 0;
  font-size: var(--fs-sm);
}
.chat-title { font-size: var(--fs-md); font-weight: 600; color: var(--c-ink); }
.chat-model {
  font-family: monospace; font-size: var(--fs-xs); color: #065f46;
  background: #d1fae5; padding: 2px 6px; border-radius: var(--r-sm);
}
.prompt-size {
  font-size: var(--fs-xs); color: #1e40af; background: #eff6ff;
  padding: 2px 6px; border-radius: var(--r-sm); font-variant-numeric: tabular-nums;
  cursor: help;
}
.prompt-size.warn {
  color: var(--c-warn); background: #fffbeb; border: 1px solid #fde68a;
}
.env-wrap { display: flex; align-items: center; gap: 4px; color: var(--c-muted); }
.env-label { font-size: var(--fs-xs); }
.env-sel {
  font-size: var(--fs-xs); padding: 3px 6px; border: 1px solid var(--c-line-2);
  border-radius: var(--r-sm); background: var(--c-surf);
}
.chat-path {
  flex: 1; font-family: monospace; font-size: var(--fs-xs); color: var(--c-muted);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.btn-clear { background: transparent; color: var(--c-muted); border-color: var(--c-line-2); }
.btn-clear:hover:not(:disabled) { color: var(--c-danger); border-color: var(--c-danger-border); }

.messages {
  flex: 1; overflow-y: auto; padding: 20px 24px;
  display: flex; flex-direction: column; gap: 14px;
}
.msg {
  max-width: 80%; padding: 10px 14px; border-radius: 10px;
  line-height: 1.65; font-size: var(--fs-base); word-break: break-word;
}
.msg.user {
  background: var(--c-accent); color: #fff; margin-left: auto;
  border-bottom-right-radius: 4px; white-space: pre-wrap;
}
.msg.bot {
  background: var(--c-surf-2); color: var(--c-ink); border: 1px solid var(--c-line);
  border-bottom-left-radius: 4px;
}
.msg.bot.welcome { background: #eff6ff; border-color: #bfdbfe; color: #1e40af; }
.welcome-hint { margin-top: 8px; font-size: var(--fs-sm); color: var(--c-muted); }
.welcome-hint code { background: rgba(0,0,0,0.06); padding: 1px 5px; border-radius: 3px; }

.md :deep(p) { margin: 0 0 8px; }
.md :deep(p:last-child) { margin-bottom: 0; }
.md :deep(h1), .md :deep(h2), .md :deep(h3) { margin: 10px 0 6px; font-weight: 600; }
.md :deep(h1) { font-size: 17px; }
.md :deep(h2) { font-size: 15px; }
.md :deep(h3) { font-size: 14px; }
.md :deep(ul), .md :deep(ol) { margin: 4px 0 8px 20px; }
.md :deep(li) { margin-bottom: 2px; }
.md :deep(code) {
  background: var(--c-surf-3); padding: 1px 5px; border-radius: var(--r-sm);
  font-family: 'SFMono-Regular', Menlo, monospace; font-size: 12.5px; color: #be185d;
}
.md :deep(pre) {
  background: #1e293b; color: #e2e8f0; padding: 10px 12px; border-radius: var(--r-md);
  overflow-x: auto; margin: 6px 0;
}
.md :deep(pre code) { background: transparent; color: inherit; padding: 0; font-size: 12.5px; }
.md :deep(strong) { font-weight: 600; }
.md :deep(a) { color: var(--c-accent); text-decoration: none; }
.md :deep(a:hover) { text-decoration: underline; }

.cursor {
  display: inline-block; width: 8px; height: 14px; background: var(--c-muted);
  vertical-align: text-bottom; animation: blink 1s infinite; margin-left: 2px;
}
@keyframes blink { 50% { opacity: 0; } }

.input-bar {
  display: flex; gap: 8px; padding: 14px 20px; background: #fff;
  border-top: 1px solid var(--c-line); flex-shrink: 0;
}
.input {
  flex: 1; padding: 10px 14px; border: 1px solid var(--c-line-2); border-radius: var(--r-md);
  font-size: var(--fs-base); resize: none; height: 48px; font-family: inherit;
  line-height: 1.5;
}
.input:focus { outline: none; border-color: var(--c-accent); }
.btn-stop { background: var(--c-danger); border-color: var(--c-danger); color: #fff; font-weight: 500; }
.btn-stop:hover:not(:disabled) { background: #b91c1c; border-color: #b91c1c; }

.gate {
  flex: 1; display: flex; align-items: center; justify-content: center;
  padding: var(--sp-6);
}
.gate-box {
  max-width: 480px; width: 100%; padding: 24px 28px;
  background: var(--c-surf); border: 1px solid var(--c-line);
  border-radius: var(--r-lg); box-shadow: 0 4px 12px rgba(15,23,42,0.08);
}
.gate-box.err { border-color: var(--c-danger-border); background: var(--c-danger-bg); }
.gate-box h2 { font-size: var(--fs-lg); margin-bottom: 10px; color: var(--c-ink); font-weight: 600; }
.gate-desc { color: var(--c-text); line-height: 1.6; margin-bottom: 14px; font-size: var(--fs-base); }
.gate-desc code { background: var(--c-surf-3); padding: 1px 5px; border-radius: var(--r-sm); }
.gate-actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 14px; }
.key-input {
  width: 100%; padding: 10px 12px; border: 1px solid var(--c-line-2);
  border-radius: var(--r-md); font-family: monospace; font-size: var(--fs-base);
  margin-bottom: 10px;
}
.key-input:focus { outline: none; border-color: var(--c-accent); }
.remember {
  display: flex; align-items: center; gap: 6px;
  font-size: var(--fs-sm); color: var(--c-muted);
}
.err-detail {
  background: #fff; border: 1px solid var(--c-danger-border); border-radius: var(--r-sm);
  padding: 10px 12px; font-family: monospace; font-size: var(--fs-xs);
  color: var(--c-danger); white-space: pre-wrap; word-break: break-word;
  max-height: 220px; overflow: auto; margin-bottom: 10px;
}
.loading-anim { display: flex; align-items: center; gap: 10px; padding: 14px 0; justify-content: center; color: var(--c-text); }
.spinner {
  width: 18px; height: 18px; border-radius: 50%;
  border: 2px solid #dbeafe; border-top-color: var(--c-accent);
  animation: spin 0.8s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }
</style>
