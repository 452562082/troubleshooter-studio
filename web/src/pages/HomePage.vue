<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import yaml from 'js-yaml'

const router = useRouter()

// 向导草稿状态（来自 InitPage 的 localStorage）
const wizardDraft = ref<any>(null)
// 当前系统名（从草稿解析）
const draftSystemName = computed(() => wizardDraft.value?.system?.name || wizardDraft.value?.system?.id || null)
const draftStep = computed(() => wizardDraft.value?.currentStep ?? null)

// 最近粘贴过的 YAML（EditorPage 也存一份，见同样 key 约定）
const lastYamlSignature = ref<{ ok: boolean; name: string; targets: string[] } | null>(null)

function loadWizard() {
  try {
    const raw = localStorage.getItem('tsf-init-wizard-v1')
    if (raw) wizardDraft.value = JSON.parse(raw)
  } catch { /* ignore */ }
}

function loadLastYaml() {
  try {
    const raw = localStorage.getItem('tsf-last-yaml')
    if (!raw) return
    const parsed: any = yaml.load(raw)
    if (parsed && typeof parsed === 'object') {
      lastYamlSignature.value = {
        ok: Boolean(parsed.system?.id),
        name: parsed.system?.name || parsed.system?.id || '未命名',
        targets: parsed.generation?.targets || ['openclaw'],
      }
    }
  } catch { /* 解析失败不显示 */ }
}

onMounted(() => {
  loadWizard()
  loadLastYaml()
})

const cards = [
  { path: '/init', icon: '🧙', label: '创建向导', desc: '7 步生成 system.yaml（支持导入已有 yaml 继续编辑）', tag: '推荐新用户' },
  { path: '/editor', icon: '📝', label: 'YAML 编辑器', desc: '直接手写 / 粘贴 system.yaml，一键验证、plan、gen' },
  { path: '/analyze', icon: '🔍', label: '仓库分析', desc: '扫描代码抽取 service_names 与配置中心线索（可选）' },
  { path: '/plan', icon: '📋', label: '计划预览', desc: '干跑一次 gen，看会生成哪些 skill / 文件 / 保留' },
  { path: '/gen', icon: '🚀', label: '生成产物', desc: '按 generation.targets 真落盘 4 种形态的机器人产物（OpenClaw / Claude Code / Cursor / Standalone）' },
  { path: '/doctor', icon: '🩺', label: '健康检查', desc: '对比声明 vs 代码实态，给 actionable 的修复建议' },
  { path: '/diff', icon: '🔀', label: '差异对比', desc: '精确到行级的新旧产物 diff，review 用' },
]

// 推荐下一步逻辑
const nextStep = computed(() => {
  if (!wizardDraft.value && !lastYamlSignature.value) {
    return { text: '从「创建向导」开始，30 秒生成第一份 system.yaml', path: '/init', cta: '开始向导 →' }
  }
  if (wizardDraft.value && draftStep.value && draftStep.value < 7) {
    return { text: `向导进行到第 ${draftStep.value} / 7 步（${draftSystemName.value || '未命名'}），回去继续？`, path: '/init', cta: '继续向导 →' }
  }
  if (wizardDraft.value && draftStep.value === 7) {
    return { text: `向导已到预览步骤，下一步是下载 yaml 并去 Editor / Gen 执行`, path: '/init', cta: '查看向导预览 →' }
  }
  if (lastYamlSignature.value?.ok) {
    return { text: `你最近编辑过 ${lastYamlSignature.value.name}（targets: ${lastYamlSignature.value.targets.join(', ')}），可以直接去 Editor 验证 / Gen 落盘`, path: '/editor', cta: '继续编辑 →' }
  }
  return { text: '从「创建向导」开始', path: '/init', cta: '开始向导 →' }
})
</script>

<template>
  <div class="home-page">
    <div class="hero">
      <h1>AI 排障机器人工作台</h1>
      <p class="tagline">AI 排障机器人工作台：为你的业务系统建模 → 生成 → 一键部署 → 后续管理。4 种 AI 平台（OpenClaw / Claude Code / Cursor / Standalone），一份 system.yaml 全覆盖。</p>
    </div>

    <!-- 推荐下一步面板 -->
    <div class="next-panel">
      <div class="next-head">
        <span class="next-icon">→</span>
        <span class="next-title">下一步推荐</span>
      </div>
      <div class="next-body">{{ nextStep.text }}</div>
      <button class="next-cta" @click="router.push(nextStep.path)">{{ nextStep.cta }}</button>
    </div>

    <!-- 能力概览 -->
    <h2 class="section-title">工作流</h2>
    <p class="section-hint">典型顺序：创建 → 编辑 → 分析（可选）→ 预览 → 生成并部署 → 健康检查。每个页面都可独立使用。</p>
    <div class="nav-card-grid">
      <div v-for="(c, i) in cards" :key="c.path" class="nav-card" @click="router.push(c.path)">
        <div class="nav-card-head">
          <span class="nav-card-idx">{{ i + 1 }}</span>
          <span class="nav-card-icon">{{ c.icon }}</span>
          <span class="nav-card-label">{{ c.label }}</span>
          <span v-if="c.tag" class="nav-card-tag">{{ c.tag }}</span>
        </div>
        <div class="nav-card-desc">{{ c.desc }}</div>
      </div>
    </div>

    <!-- 快速了解 -->
    <h2 class="section-title">想了解更多</h2>
    <div class="info-grid">
      <div class="info-card">
        <div class="info-head">4 种部署形态</div>
        <ul>
          <li><code>openclaw</code> — bash install.sh 部署到 OpenClaw</li>
          <li><code>claude-code</code> — CLAUDE.md + skills/ 装到项目根</li>
          <li><code>cursor</code> — .cursorrules + .cursor/rules/</li>
          <li><code>standalone</code> — Flask + Docker 独立 Web 聊天</li>
        </ul>
      </div>
      <div class="info-card">
        <div class="info-head">CLI 命令速查</div>
        <ul>
          <li><code>tshoot demo</code> — 零配置试跑（30 秒看产物）</li>
          <li><code>tshoot init -o system.yaml</code> — 命令行向导</li>
          <li><code>tshoot discover</code> — 扫本机已装机器人</li>
          <li><code>tshoot gen -i ...</code> — 生成产物</li>
          <li><code>tshoot apply -i new.yaml --path ...</code> — 原地更新已装的机器人</li>
        </ul>
      </div>
    </div>
  </div>
</template>

<style scoped>
.home-page { max-width: 920px; margin: 0 auto; padding: var(--sp-6) 28px; }

/* h1 用 tokens 基准 xl;hero 需要再大一档,单独调 */
.hero h1 { font-size: 26px; color: var(--c-ink); margin-bottom: 6px; font-weight: 600; }
.tagline { color: var(--c-muted); font-size: var(--fs-md); margin-bottom: var(--sp-6); line-height: 1.6; }

/* 下一步推荐 */
.next-panel {
  background: linear-gradient(90deg, #eff6ff 0%, #f0f9ff 100%);
  border: 1px solid #bfdbfe;
  border-left: 4px solid var(--c-accent);
  border-radius: 10px;
  padding: var(--sp-4) var(--sp-5);
  margin-bottom: 32px;
  display: flex;
  flex-direction: column;
  gap: var(--sp-2);
}
.next-head { display: flex; align-items: center; gap: var(--sp-2); }
.next-icon { color: var(--c-accent); font-weight: 700; font-size: var(--fs-md); }
.next-title { font-weight: 600; color: #1e40af; font-size: var(--fs-md); }
.next-body { color: var(--c-ink); font-size: var(--fs-md); line-height: 1.6; }
.next-cta {
  align-self: flex-start;
  background: var(--c-accent); color: #fff; border: none;
  padding: 8px 18px; border-radius: var(--r-md); font-size: var(--fs-md); font-weight: 500;
  cursor: pointer; transition: background 0.15s;
}
.next-cta:hover { background: var(--c-accent-hover); }

/* sections */
.section-title { font-size: var(--fs-lg); color: var(--c-ink); margin: var(--sp-3) 0 6px; font-weight: 600; }
.section-hint { color: var(--c-muted); font-size: var(--fs-base); margin-bottom: var(--sp-3); }

/* 工作流卡片网格 */
.nav-card-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: var(--sp-3); margin-bottom: 32px; }
.nav-card {
  border: 1px solid var(--c-line); border-radius: var(--r-lg); padding: var(--sp-3) var(--sp-4);
  background: var(--c-surf); cursor: pointer; transition: all 0.15s;
  display: flex; flex-direction: column; gap: 6px;
}
.nav-card:hover { border-color: var(--c-accent); box-shadow: 0 2px 6px rgba(59,130,246,0.1); transform: translateY(-1px); }
.nav-card-head { display: flex; align-items: center; gap: var(--sp-2); }
.nav-card-idx {
  width: 20px; height: 20px; border-radius: 50%;
  background: var(--c-line); color: #475569; font-size: var(--fs-xs); font-weight: 700;
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
}
.nav-card-icon { font-size: var(--fs-lg); }
.nav-card-label { font-weight: 600; color: var(--c-ink); font-size: var(--fs-md); flex: 1; }
.nav-card-tag { font-size: 10px; font-weight: 700; color: var(--c-ink); background: #f59e0b; padding: 2px 7px; border-radius: var(--r-lg); }
.nav-card-desc { color: var(--c-muted); font-size: var(--fs-sm); line-height: 1.55; }

/* info cards */
.info-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--sp-4); margin-bottom: 32px; }
.info-card { border: 1px solid var(--c-line); border-radius: var(--r-lg); padding: var(--sp-3) 18px; background: var(--c-surf-2); }
.info-head { font-weight: 600; color: var(--c-ink); font-size: var(--fs-md); margin-bottom: var(--sp-2); }
.info-card ul { margin: 0; padding-left: 18px; }
.info-card li { color: #475569; font-size: var(--fs-base); line-height: 1.8; }
/* 这里的 code 想要蓝色强调,不走全局灰色 */
.info-card code { background: rgba(0,0,0,0.06); color: #1e40af; }

@media (max-width: 680px) {
  .info-grid { grid-template-columns: 1fr; }
}
</style>
