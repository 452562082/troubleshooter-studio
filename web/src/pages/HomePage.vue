<script setup lang="ts">
import { ref, computed, onMounted, onActivated } from 'vue'
import { useRouter } from 'vue-router'
import yaml from 'js-yaml'
import brandLogo from '../assets/logo.svg'
import { confirmDialog } from '../lib/confirm'

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

// App.vue 用 <keep-alive> 缓存 router-view → HomePage 只 mount 一次,onMounted 只触发一次。
// 用户在 InitPage 改了草稿(步号 / 系统名)再切回首页,不刷新就还是看到旧值。
// 改用 onActivated:每次 keep-alive 重新激活本组件时重读 localStorage,所有派生
// computed(draftStep / draftSystemName / nextStep)自动更新。
function refreshDraftSnapshot() {
  loadWizard()
  loadLastYaml()
}
onMounted(refreshDraftSnapshot)
onActivated(refreshDraftSnapshot)

// "创建向导"卡跟"下一步推荐.继续向导"语义不同:
//   - 创建向导卡 = 想新建一个机器人(若有草稿,先问要不要清空重开)
//   - 继续向导 = 接着上次草稿往下走(直接 router.push)
// 之前两个按钮都只是 router.push('/init') 没区别,现在拆出来:
//   - 卡片有草稿时弹 confirm 让用户挑"清空重开 / 继续之前的"
//   - 卡片没草稿直接跳
async function onPrimaryCardClick(card: { path: string; label: string }) {
  if (card.path === '/init' && wizardDraft.value) {
    // confirmText='清空重开'(危险红色按钮),cancelText='继续之前的'(安全侧)。
    // defaultAction='cancel' 让自动聚焦 / Enter 命中"继续之前的",Esc / 点遮罩也落到
    // 继续之前的一侧。用户必须用鼠标显式点红色按钮才会真清空,符合"危险动作要确认"。
    const wipe = await confirmDialog({
      title: '已有未完成的向导草稿',
      message: `「${draftSystemName.value || '未命名'}」上次填到第 ${draftStepNormalized.value} / ${WIZARD_TOTAL_STEPS} 步,要继续吗?`,
      confirmText: '清空重开',
      cancelText: '继续之前的',
      danger: true,
      defaultAction: 'cancel',
    })
    if (wipe) {
      // 二次确认:清空草稿是不可逆操作,误点代价是用户前面填的几步全丢。
      // 默认动作仍是"取消"(safe by default),用户必须显式再点一次红色"确认清空"。
      const reallyWipe = await confirmDialog({
        title: '再确认一次',
        message: '点击"确认清空"会丢弃当前草稿,无法恢复。',
        confirmText: '确认清空',
        cancelText: '不,我再想想',
        danger: true,
        defaultAction: 'cancel',
      })
      if (reallyWipe) {
        try { localStorage.removeItem('tsf-init-wizard-v1') } catch { /* ignore */ }
        wizardDraft.value = null
      }
    }
    router.push(card.path)
    return
  }
  router.push(card.path)
}

// 主路径卡片:80% 用户的核心工作流。顺序按"新用户第一次打开 → 老用户管理"设计,
// 已装机器人放 #2 是因为"我已有 yaml 想直接部署"和"查看/改已装"都在那页。
// 注:不再列"生成产物"独立页。桌面端的完整流已经是:
//   创建向导(产 yaml) → 已装机器人(导入 yaml 一键部署 + 原地管理)
// 纯落盘产物需求走 CLI:`tshoot gen -i system.yaml -o ./dist/<id>`
const primaryCards = [
  { path: '/init', icon: '🧙', label: '创建向导', desc: '一步步带你创建一个新的排障机器人', tag: '推荐新用户' },
  { path: '/bots', icon: '🤖', label: '已装机器人', desc: '管理已部署的机器人,新建、更新或重新部署' },
]
// 诊断工具:YAML 沙盒 + 代码扫描。两者职责对齐(2026-04-30):
//   YAML 沙盒  → 操作 yaml 文件(验证/生成预览/产物预览)
//   代码扫描  → 操作代码仓库(扫码反推 yaml,可应用差异回 yaml 形成闭环)
// Doctor 已合进 BotsPage 卡片,独立页已删。
const advancedCards = [
  { path: '/editor',  icon: '📝', label: 'YAML 沙盒', desc: '验证 yaml + 干跑生成 + 产物预览' },
  { path: '/analyze', icon: '🔍', label: '代码扫描', desc: '扫仓库反推服务 / 配置,可应用回 yaml' },
]

// InitPage 的步骤布局(2026-04-30 版,共 10 步):
//   1=欢迎  2=系统  3=机器人  4=环境  5=仓库  6=配置源  7=数据层  8=可观测  9=预览生成  10=一键部署
// 跟 InitPage.vue::totalSteps 同步。改 totalSteps 时这里要一起改。
const WIZARD_TOTAL_STEPS = 10
const WIZARD_PREVIEW_STEP = 9
const WIZARD_DEPLOY_STEP = 10

// draftStep 兼容老 saved:老版本(8/9 步制)的 saved.currentStep 没经过 wizardSchema=2 标记
// 时 InitPage 加载会 +1 一次性迁移。本页只读不写,所以用 saved.wizardSchema 判断 → 老 saved
// 显示的步号也跟着 +1,跟 InitPage 渲染保持一致。
const draftStepNormalized = computed<number | null>(() => {
  if (!wizardDraft.value || draftStep.value == null) return null
  const schema = (wizardDraft.value.wizardSchema ?? 1) as number
  return schema >= 2 ? draftStep.value : Math.min(draftStep.value + 1, WIZARD_TOTAL_STEPS)
})

// 推荐下一步逻辑
const nextStep = computed(() => {
  if (!wizardDraft.value && !lastYamlSignature.value) {
    return { text: '从「创建向导」开始，30 秒生成第一份 system.yaml', path: '/init', cta: '开始向导 →' }
  }
  const step = draftStepNormalized.value
  if (wizardDraft.value && step != null && step < WIZARD_PREVIEW_STEP) {
    return { text: `向导进行到第 ${step} / ${WIZARD_TOTAL_STEPS} 步（${draftSystemName.value || '未命名'}），回去继续？`, path: '/init', cta: '继续向导 →' }
  }
  if (wizardDraft.value && step === WIZARD_PREVIEW_STEP) {
    return { text: '向导已到预览步骤，确认 yaml 后下一步即可一键部署', path: '/init', cta: '查看向导预览 →' }
  }
  if (wizardDraft.value && step === WIZARD_DEPLOY_STEP) {
    return { text: '向导已到一键部署步,直接装机即可', path: '/init', cta: '继续部署 →' }
  }
  if (lastYamlSignature.value?.ok) {
    return { text: `你最近编辑过 ${lastYamlSignature.value.name}（targets: ${lastYamlSignature.value.targets.join(', ')}），可以直接去 YAML 沙盒验证 / 部署`, path: '/editor', cta: '继续编辑 →' }
  }
  return { text: '从「创建向导」开始', path: '/init', cta: '开始向导 →' }
})
</script>

<template>
  <div class="home-page">
    <div class="hero">
      <img :src="brandLogo" class="hero-logo" alt="Troubleshooter Studio" />
      <p class="tagline">为你的业务系统快速生成并部署 AI 排障机器人。</p>
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

    <!-- 主路径 -->
    <h2 class="section-title">主路径</h2>
    <p class="section-hint">典型流:创建 yaml → 编辑 / 生成 → 导入或部署到机器人。每个页面可独立使用。</p>
    <div class="nav-card-grid">
      <div
        v-for="(c, i) in primaryCards"
        :key="c.path"
        class="nav-card primary"
        @click="onPrimaryCardClick(c)"
      >
        <div class="nav-card-head">
          <span class="nav-card-idx">{{ i + 1 }}</span>
          <span class="nav-card-icon">{{ c.icon }}</span>
          <span class="nav-card-label">{{ c.label }}</span>
          <span v-if="c.tag" class="nav-card-tag">{{ c.tag }}</span>
        </div>
        <div class="nav-card-desc">
          {{ c.desc }}
          <!-- 创建向导卡上,有草稿时给"已有草稿"标识,让用户清楚点这个会进入"清空重开 / 继续"二选一 -->
          <span v-if="c.path === '/init' && wizardDraft" class="nav-card-draft-badge">
            · 已有草稿(第 {{ draftStepNormalized }} / {{ WIZARD_TOTAL_STEPS }} 步)
          </span>
        </div>
      </div>
    </div>

    <!-- 高级 / 诊断:折叠入口弱化,避免跟主路径抢视觉焦点 -->
    <h2 class="section-title secondary">高级 · 诊断</h2>
    <p class="section-hint">辅助工具:YAML 沙盒(yaml 文件验证/预览)、代码扫描(从代码反推配置,可应用回 yaml)。</p>
    <div class="nav-card-grid compact">
      <div v-for="c in advancedCards" :key="c.path" class="nav-card advanced" @click="router.push(c.path)">
        <div class="nav-card-head">
          <span class="nav-card-icon">{{ c.icon }}</span>
          <span class="nav-card-label">{{ c.label }}</span>
        </div>
        <div class="nav-card-desc">{{ c.desc }}</div>
      </div>
    </div>

    <!-- 快速了解 -->
    <h2 class="section-title">想了解更多</h2>
    <div class="info-grid">
      <div class="info-card">
        <div class="info-head">部署形态</div>
        <ul>
          <li><code>openclaw</code> — 装到 <code>~/.openclaw/workspace/</code>,在 OpenClaw 客户端里选 agent 对话</li>
          <li><code>claude-code</code> — 装到 <code>~/.claude/agents/</code>,任意项目里 @&lt;name&gt; 调用</li>
          <li><code>cursor</code> — 装到 <code>~/.cursor/agents/</code>,Cursor AI 侧栏选用</li>
        </ul>
      </div>
      <div class="info-card">
        <div class="info-head">CLI 命令速查</div>
        <ul>
          <li><code>tshoot demo</code> — 零配置试跑（30 秒看产物）</li>
          <li><code>tshoot init -o system.yaml</code> — 命令行向导</li>
          <li><code>tshoot gen -i ...</code> — 生成 staging 产物</li>
          <li><code>tshoot install --path ... --target ...</code> — 装到本机(原生 Go,无 bash)</li>
          <li><code>tshoot discover</code> — 扫本机已装机器人</li>
          <li><code>tshoot apply -i new.yaml --path ...</code> — 原地更新已装的机器人</li>
        </ul>
      </div>
    </div>
  </div>
</template>

<style scoped>
.home-page { max-width: 920px; margin: 0 auto; padding: var(--sp-6) 28px; }

/* hero 品牌 logo:简化版 svg(560×140,studio icon + 箭头 + 机器人 + 双行 wordmark);
 * height 控制视觉密度,缩小到 84px 让首屏内容更靠上。viewBox 内左侧已有 16px
 * 内边距,这里 margin-left 不再硬拉,跟正文文本同左缘对齐即可。 */
.hero-logo {
  display: block;
  height: 84px; width: auto; max-width: 100%;
  margin-bottom: 4px;
}
.tagline { color: var(--c-muted); font-size: var(--fs-md); margin-bottom: var(--sp-6); line-height: 1.6; }

/* 下一步推荐:首屏的主 CTA。之前是浅蓝渐变,跟普通 info 框视觉优先级打平,
 * 非程序员首次打开看不出主路径。现在改深蓝 + 白字 + 更明显阴影,
 * CTA 按钮反色(白底蓝字)制造层级对比,一眼抓住视线。 */
.next-panel {
  background: linear-gradient(135deg, #2563eb 0%, #1d4ed8 100%);
  border: none;
  border-radius: 12px;
  padding: 20px 24px;
  margin-bottom: 36px;
  display: flex;
  flex-direction: column;
  gap: var(--sp-3);
  box-shadow: 0 6px 18px rgba(37, 99, 235, 0.25), 0 2px 4px rgba(37, 99, 235, 0.15);
}
.next-head { display: flex; align-items: center; gap: var(--sp-2); }
.next-icon {
  display: inline-flex; align-items: center; justify-content: center;
  width: 24px; height: 24px; border-radius: 50%;
  background: rgba(255, 255, 255, 0.2); color: #fff; font-weight: 700; font-size: var(--fs-md);
}
.next-title { font-weight: 600; color: #fff; font-size: var(--fs-md); letter-spacing: 0.3px; }
.next-body { color: #f1f5f9; font-size: var(--fs-md); line-height: 1.6; }
.next-cta {
  align-self: flex-start;
  background: #fff; color: #1d4ed8; border: none;
  padding: 10px 20px; border-radius: var(--r-md); font-size: var(--fs-md); font-weight: 600;
  cursor: pointer; transition: transform 0.15s, box-shadow 0.15s;
  box-shadow: 0 2px 4px rgba(0, 0, 0, 0.08);
}
.next-cta:hover { transform: translateY(-1px); box-shadow: 0 4px 8px rgba(0, 0, 0, 0.12); }
.next-cta:active { transform: translateY(0); }

/* sections */
.section-title { font-size: var(--fs-lg); color: var(--c-ink); margin: var(--sp-3) 0 6px; font-weight: 600; }
.section-title.secondary { font-size: var(--fs-md); color: var(--c-muted); font-weight: 500; margin-top: 36px; }
.section-hint { color: var(--c-muted); font-size: var(--fs-base); margin-bottom: var(--sp-3); }

/* 工作流卡片网格 */
.nav-card-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: var(--sp-3); margin-bottom: 32px; }
/* compact:高级/诊断用小一号密度,视觉上让开主路径 */
.nav-card-grid.compact { grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: var(--sp-2); }
.nav-card {
  border: 1px solid var(--c-line); border-radius: var(--r-lg); padding: var(--sp-3) var(--sp-4);
  background: var(--c-surf); cursor: pointer; transition: all 0.15s;
  display: flex; flex-direction: column; gap: 6px;
}
.nav-card:hover { border-color: var(--c-accent); box-shadow: 0 2px 6px rgba(59,130,246,0.1); transform: translateY(-1px); }
/* primary:主路径卡片,边框略深 + hover 动效更明显。 */
.nav-card.primary { border-color: var(--c-line-2); }
.nav-card.primary:hover { box-shadow: 0 4px 10px rgba(59,130,246,0.15); }
/* advanced:高级/诊断卡片,内边距减一档 + 文字色弱化,划分层级但仍可点 */
.nav-card.advanced { padding: var(--sp-2) var(--sp-3); background: var(--c-surf-2); }
.nav-card.advanced .nav-card-label { color: var(--c-text); font-weight: 500; }
.nav-card.advanced .nav-card-desc { font-size: var(--fs-xs); color: var(--c-muted); }
.nav-card.advanced:hover { background: var(--c-surf); }

.nav-card-head { display: flex; align-items: center; gap: var(--sp-2); }
.nav-card-idx {
  width: 22px; height: 22px; border-radius: 50%;
  background: #dbeafe; color: #1e40af; font-size: var(--fs-xs); font-weight: 700;
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
}
.nav-card-icon { font-size: var(--fs-lg); }
.nav-card-label { font-weight: 600; color: var(--c-ink); font-size: var(--fs-md); flex: 1; }
.nav-card-tag { font-size: 10px; font-weight: 700; color: var(--c-ink); background: #f59e0b; padding: 2px 7px; border-radius: var(--r-lg); }
.nav-card-desc { color: var(--c-muted); font-size: var(--fs-sm); line-height: 1.55; }
.nav-card-draft-badge {
  display: inline-block;
  margin-left: 4px;
  padding: 1px 6px;
  border-radius: 3px;
  background: #fef3c7;
  color: #92400e;
  font-size: 11px;
  font-weight: 500;
}

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
