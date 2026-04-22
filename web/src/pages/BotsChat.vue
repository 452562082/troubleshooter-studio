<script setup lang="ts">
// 嵌入式 chat 视图:跟 standalone 机器人对话,不用开浏览器。
//
// 流程:
//   1. 从 router.query.path 拿机器人产物目录
//   2. 调 StandaloneStatus 看 runner 活着没
//   3. 不活 / 没起:要 LLM_API_KEY(env 有就不问,没有就弹 key 表单)→ StartStandalone
//   4. 拿到 port → iframe src="http://localhost:<port>"
//
// 如果 server.py 某天自己挂了(比如 API key 额度 用完),iframe 会显示
// Chrome 的 "ERR_CONNECTION_REFUSED",用户点"重连"我们重新 Status+Start。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  isDesktop,
  revealInFinder,
  standaloneStatus,
  startStandalone,
  stopStandalone,
} from '../lib/bridge'
import { toast } from '../lib/toast'

const route = useRoute()
const router = useRouter()

const botPath = computed(() => String(route.query.path || ''))
const botName = computed(() => String(route.query.name || '排障机器人'))

type Stage = 'init' | 'need-key' | 'starting' | 'ready' | 'error'
const stage = ref<Stage>('init')
const port = ref<number | null>(null)
const apiKeyInput = ref('')
const rememberKey = ref(true) // 本会话记住,避免每次返回都重输
const errMsg = ref<string | null>(null)
const launchStartTime = ref<number | null>(null)
const launchElapsed = ref(0)
let launchTimer: number | null = null

// 本会话级的 key 记忆:页间切换回来不用重问;window 关了(app 退出)就没了,
// Studio 没做 secure key store 之前这样就够 —— 也是安全的折衷(不落盘)。
const sessionKeyStore = (window as any).__tshootStandaloneKeys__ ??= {} as Record<string, string>

function redirectIfInvalid() {
  if (!isDesktop()) {
    errMsg.value = '嵌入式对话只在桌面 app 里可用;浏览器模式请直接打开 standalone 机器人产物的 README 走独立部署'
    stage.value = 'error'
    return false
  }
  if (!botPath.value) {
    toast.error('缺 path 参数,回已装机器人页')
    router.replace('/bots')
    return false
  }
  return true
}

async function init() {
  if (!redirectIfInvalid()) return
  try {
    const st = await standaloneStatus(botPath.value)
    if (st.running && st.port) {
      port.value = st.port
      stage.value = 'ready'
      return
    }
    // 没跑起来 —— 看会话 key 缓存,有直接启,没有让用户填
    const cached = sessionKeyStore[botPath.value]
    if (cached) {
      await doStart(cached)
    } else {
      stage.value = 'need-key'
    }
  } catch (e: any) {
    errMsg.value = String(e?.message || e)
    stage.value = 'error'
  }
}

async function submitKey() {
  if (!apiKeyInput.value.trim()) {
    errMsg.value = 'LLM_API_KEY 不能为空'
    return
  }
  await doStart(apiKeyInput.value.trim())
}

async function doStart(apiKey: string) {
  errMsg.value = null
  stage.value = 'starting'
  launchStartTime.value = Date.now()
  launchElapsed.value = 0
  if (launchTimer) clearInterval(launchTimer)
  launchTimer = window.setInterval(() => {
    if (launchStartTime.value) {
      launchElapsed.value = Math.floor((Date.now() - launchStartTime.value) / 1000)
    }
  }, 1000)
  try {
    const res = await startStandalone(botPath.value, apiKey)
    port.value = res.port
    if (rememberKey.value) sessionKeyStore[botPath.value] = apiKey
    stage.value = 'ready'
  } catch (e: any) {
    errMsg.value = String(e?.message || e)
    stage.value = 'error'
  } finally {
    if (launchTimer) { clearInterval(launchTimer); launchTimer = null }
    launchStartTime.value = null
  }
}

async function stop() {
  await stopStandalone(botPath.value)
  port.value = null
  stage.value = 'init'
  toast.info('已停止 standalone 机器人')
  router.replace('/bots')
}

function retry() { init() }

const chatUrl = computed(() => (port.value ? `http://localhost:${port.value}/` : ''))

onMounted(init)
onBeforeUnmount(() => {
  if (launchTimer) clearInterval(launchTimer)
  // 注意:不在 unmount 里 stop runner —— 用户只是切页,runner 保持在跑,
  // 下次进 chat 页复用;app 退出时 main defer 会清一锅。
})

// 同一浏览器路由在切换不同 path 时重初始化
watch(() => botPath.value, (v, old) => { if (v !== old) init() })
</script>

<template>
  <div class="chat-page">
    <header class="chat-head">
      <button class="btn small" @click="router.push('/bots')">← 返回已装机器人</button>
      <span class="chat-title">{{ botName }}</span>
      <span class="chat-path" :title="botPath">📁 {{ botPath }}</span>
      <span v-if="port" class="chat-port">:{{ port }}</span>
      <div class="chat-actions">
        <button v-if="stage === 'ready'" class="btn small" @click="revealInFinder(botPath)">在 Finder 显示</button>
        <button v-if="stage === 'ready'" class="btn small btn-stop" @click="stop">停止</button>
      </div>
    </header>

    <!-- 需要 key:内联表单 -->
    <div v-if="stage === 'need-key'" class="gate">
      <div class="gate-box">
        <h2>启动 {{ botName }}</h2>
        <p class="gate-desc">
          standalone 机器人需要一个 <code>LLM_API_KEY</code>(Anthropic API Key)来调模型。
          本会话输一次后会记住,app 关闭即清。
        </p>
        <input
          v-model="apiKeyInput"
          type="password"
          class="key-input"
          placeholder="sk-ant-api03-..."
          @keyup.enter="submitKey"
        />
        <label class="remember">
          <input type="checkbox" v-model="rememberKey" />
          本会话记住(不落盘)
        </label>
        <div v-if="errMsg" class="alert error">{{ errMsg }}</div>
        <div class="gate-actions">
          <button class="btn" @click="router.push('/bots')">取消</button>
          <button class="btn primary" :disabled="!apiKeyInput.trim()" @click="submitKey">启动</button>
        </div>
      </div>
    </div>

    <!-- 启动中 -->
    <div v-else-if="stage === 'starting' || stage === 'init'" class="gate">
      <div class="gate-box">
        <div class="starting-anim">
          <div class="launch-spinner"></div>
          <div class="starting-text">
            {{ stage === 'starting' ? `正在启动 server.py... ${launchElapsed}s` : '检查状态...' }}
          </div>
        </div>
        <p class="gate-hint">冷启动要 import flask + anthropic,一般 3-8 秒。</p>
      </div>
    </div>

    <!-- 报错 -->
    <div v-else-if="stage === 'error'" class="gate">
      <div class="gate-box error">
        <h2>⚠ 启动失败</h2>
        <pre class="err-detail">{{ errMsg }}</pre>
        <div class="gate-actions">
          <button class="btn" @click="router.push('/bots')">返回</button>
          <button class="btn primary" @click="retry">重试</button>
        </div>
      </div>
    </div>

    <!-- 就绪:iframe 嵌入 standalone 聊天 UI -->
    <iframe
      v-else-if="stage === 'ready' && chatUrl"
      :src="chatUrl"
      class="chat-frame"
      :title="botName"
    />
  </div>
</template>

<style scoped>
.chat-page {
  display: flex; flex-direction: column; height: 100%;
  /* 覆盖全局 main 的 32px padding,让 iframe 顶满 */
  margin: -32px; background: #fff;
}
.chat-head {
  display: flex; align-items: center; gap: 10px;
  padding: 10px 14px; border-bottom: 1px solid var(--c-line);
  background: var(--c-surf-2); flex-shrink: 0;
}
.chat-title { font-size: var(--fs-md); font-weight: 600; color: var(--c-ink); }
.chat-path {
  flex: 1; font-family: monospace; font-size: var(--fs-xs); color: var(--c-muted);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.chat-port {
  font-family: monospace; font-size: var(--fs-xs); color: #065f46;
  background: #d1fae5; padding: 2px 6px; border-radius: var(--r-sm);
}
.chat-actions { display: flex; gap: 6px; }
.btn-stop { background: #fef2f2; border-color: #fecaca; color: #991b1b; }
.btn-stop:hover:not(:disabled) { background: #fee2e2; }

.chat-frame {
  flex: 1; width: 100%; border: none; background: #fff;
}

.gate {
  flex: 1; display: flex; align-items: center; justify-content: center;
  padding: var(--sp-6);
}
.gate-box {
  max-width: 480px; width: 100%; padding: 24px 28px;
  background: var(--c-surf); border: 1px solid var(--c-line);
  border-radius: var(--r-lg); box-shadow: 0 4px 12px rgba(15,23,42,0.08);
}
.gate-box.error { border-color: var(--c-danger-border); background: var(--c-danger-bg); }
.gate-box h2 { font-size: var(--fs-lg); margin-bottom: 10px; color: var(--c-ink); font-weight: 600; }
.gate-desc { color: var(--c-text); line-height: 1.6; margin-bottom: 14px; font-size: var(--fs-base); }
.gate-desc code { background: var(--c-surf-3); padding: 1px 5px; border-radius: var(--r-sm); }
.gate-hint { color: var(--c-muted); font-size: var(--fs-sm); margin-top: 12px; text-align: center; }
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

.starting-anim {
  display: flex; flex-direction: column; align-items: center; gap: 14px;
  padding: 20px 0;
}
.launch-spinner {
  width: 32px; height: 32px; border-radius: 50%;
  border: 3px solid #dbeafe; border-top-color: var(--c-accent);
  animation: spin 0.8s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }
.starting-text {
  font-size: var(--fs-md); color: var(--c-text); font-weight: 500;
  font-variant-numeric: tabular-nums;
}
</style>
