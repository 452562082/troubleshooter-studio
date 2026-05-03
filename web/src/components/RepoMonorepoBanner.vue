<script setup lang="ts">
// RepoMonorepoBanner —— 仓库扫描后弹出的"我看到 N 个子模块"横幅。
// 两条分支:
//   A. .gitmodules 路径(独立 git submodule)→ "拆成 N 个独立仓库条目"
//   B. 同仓多入口(monorepo)→ "合并为本仓 N 个服务名,自动加 <repo>- 前缀"
//
// 父端 RepoListItem 持有 repo reactive,把 _submoduleHints / _submoduleSelection 直传过来,
// 本组件只渲染 + emit 三个动作。

interface SubmoduleHint {
  name: string
  sub_path: string
  stack: string
  role: string
  reason: string
  url?: string
}

defineProps<{
  /** 本仓 reactive(直传引用,checkbox 勾选直接 mutate _submoduleSelection) */
  repo: {
    name: string
    _submoduleHints?: SubmoduleHint[]
    _submoduleHintsDismissed?: boolean
    _submoduleSelection?: Record<string, boolean>
  }
  /** 父行 index,emit splitMonorepo / mergeMonorepoIntoServices 时带上 */
  index: number
  /** 父端 helper:判定是否是 .gitmodules(分支 A vs 分支 B) */
  isGitSubmodulesHints: (hints: { url?: string }[]) => boolean
  /** 服务名加前缀消歧义 */
  qualifyServiceName: (repoName: string, hintName: string) => string
  /** 子模块在仓库内的完整路径(含 sub_path) */
  submodulePathFor: (r: any, sub: string) => string
  /** 当前勾选的子模块数(决定按钮文案 + 是否 disabled) */
  pickedSubmoduleCount: (r: any) => number
}>()

const emit = defineEmits<{
  toggleSubmodulePick: [r: any, subPath: string, checked: boolean]
  splitMonorepo: [idx: number]
  mergeMonorepoIntoServices: [idx: number]
}>()
</script>

<template>
  <div
    v-if="repo._submoduleHints !== undefined && !repo._submoduleHintsDismissed"
    class="monorepo-banner"
    :class="{
      'monorepo-banner-mono': repo._submoduleHints.length > 1,
      'monorepo-banner-single': repo._submoduleHints.length <= 1,
    }"
  >
    <div v-if="repo._submoduleHints.length === 0" class="monorepo-banner-head ok">
      ✓ 检测结果:单服务仓库(整仓当一个服务处理,无需拆分)
    </div>
    <div v-else-if="repo._submoduleHints.length === 1" class="monorepo-banner-head warn">
      ⚠ 仅检测到 1 个入口,看着不像 monorepo(整仓当一个服务也行)
    </div>
    <template v-else-if="isGitSubmodulesHints(repo._submoduleHints)">
      <!-- 分支 A:.gitmodules 路径 -->
      <div class="monorepo-banner-head">
        🔍 检测到 .gitmodules 声明的 {{ repo._submoduleHints.length }} 个独立子模块(每个都是独立 git repo,默认全选):
      </div>
      <div class="monorepo-banner-hint">
        💡 <strong>不点"拆分"按钮就不影响</strong> —— 如果当成单仓处理,直接在下方"服务名"里手填即可。
      </div>
      <ul class="monorepo-banner-list">
        <li v-for="h in repo._submoduleHints" :key="h.sub_path">
          <label class="monorepo-row-check">
            <input
              type="checkbox"
              :checked="repo._submoduleSelection?.[h.sub_path] !== false"
              @change="emit('toggleSubmodulePick', repo, h.sub_path, ($event.target as HTMLInputElement).checked)"
            />
            <div class="monorepo-row-content">
              <div class="monorepo-row-top">
                <strong>{{ h.name }}</strong>
                <span class="monorepo-stack">{{ h.stack }}</span>
                <span class="monorepo-role">{{ h.role }}</span>
              </div>
              <div class="monorepo-row-path">
                📂 <code>{{ submodulePathFor(repo, h.sub_path) }}</code>
              </div>
              <div class="monorepo-row-url" :title="h.url">
                🔗 <code>{{ h.url }}</code>
                <span class="field-hint">(独立 git repo)</span>
              </div>
              <div class="monorepo-row-reason">
                <span class="field-hint">{{ h.reason }}</span>
              </div>
            </div>
          </label>
        </li>
      </ul>
      <button
        type="button"
        class="btn primary monorepo-split-btn"
        :disabled="pickedSubmoduleCount(repo) === 0"
        @click="emit('splitMonorepo', index)"
      >
        ✂ 拆成 {{ pickedSubmoduleCount(repo) }} 个独立仓库条目(各自 url / 本地路径 / role)
      </button>
    </template>
    <template v-else>
      <!-- 分支 B:同仓多入口(合并到 service_names) -->
      <div class="monorepo-banner-head">
        🔍 在本仓检测到 {{ repo._submoduleHints.length }} 个服务入口(同一 git 仓库,多服务模式):
      </div>
      <div class="monorepo-banner-hint">
        💡 这些是<strong>同仓多服务</strong>(不是独立 git repo),建议合并到本仓的服务名列表 —— 每个服务自动加 <code>{{ repo.name || '&lt;repo&gt;' }}-</code> 前缀消歧义(避免跨仓重名,如 4 个仓都有 cmd/grpc-server 时撞成一团)。
      </div>
      <ul class="monorepo-banner-list">
        <li v-for="h in repo._submoduleHints" :key="h.sub_path">
          <label class="monorepo-row-check">
            <input
              type="checkbox"
              :checked="repo._submoduleSelection?.[h.sub_path] !== false"
              @change="emit('toggleSubmodulePick', repo, h.sub_path, ($event.target as HTMLInputElement).checked)"
            />
            <div class="monorepo-row-content">
              <div class="monorepo-row-top">
                <strong>{{ qualifyServiceName(repo.name, h.name) }}</strong>
                <span v-if="qualifyServiceName(repo.name, h.name) !== h.name" class="field-hint">
                  (原入口名:{{ h.name }})
                </span>
                <span class="monorepo-stack">{{ h.stack }}</span>
                <span class="monorepo-role">{{ h.role }}</span>
              </div>
              <div class="monorepo-row-path">
                📂 入口 <code>{{ submodulePathFor(repo, h.sub_path) }}</code>
              </div>
              <div class="monorepo-row-reason">
                <span class="field-hint">{{ h.reason }}</span>
              </div>
            </div>
          </label>
        </li>
      </ul>
      <button
        type="button"
        class="btn primary monorepo-split-btn"
        :disabled="pickedSubmoduleCount(repo) === 0"
        @click="emit('mergeMonorepoIntoServices', index)"
      >
        ➕ 合并为本仓 {{ pickedSubmoduleCount(repo) }} 个服务名(自动加 <code>{{ repo.name || '&lt;repo&gt;' }}-</code> 前缀)
      </button>
    </template>
  </div>
</template>
