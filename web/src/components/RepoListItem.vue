<script setup lang="ts">
// RepoListItem —— Step 4 仓库列表的单行编辑器(InitPage 子组件中较复杂的一个):
//   - 仓库 head(badge + sub_path 标 + 删除按钮)
//   - 来源切换(remote URL / local 已有目录)
//   - 远程模式:URL 输入 + clone 父目录 + 同步扫描按钮
//   - 本地模式:目录选择 + URL probe 反馈
//   - 仓库名 + 技术栈展示(readonly,扫描自动填)
//   - <RepoMonorepoBanner>:monorepo 检测横幅(0/1/N 子模块,A 拆分 / B 合并)
//   - 已合并 service_entries 展示
//   - sub_path 编辑(monorepo 子目录)
//   - 角色下拉 + 推荐 chip
//   - <RepoServiceChips>:服务名 chip 列表 + inline "+" 输入
//   - <RepoEnvBranches>:环境 → 分支 映射(<select> / <input> 兜底)
//
// 父端持有 repos / repoBranchesMap / svcAddInputs / reposRootInput / resolvedReposRoot 等 reactive,
// 大量 helper 通过函数 prop 传入;repo 对象 reactive 直传,v-model 写值同步回父端 repos[i]。

import RepoMonorepoBanner from './RepoMonorepoBanner.vue'
import RepoServiceChips from './RepoServiceChips.vue'
import RepoEnvBranches from './RepoEnvBranches.vue'

interface RepoItem {
  name: string
  url: string
  stack: string
  framework: string
  role?: string
  sub_path?: string
  parent_repo?: string
  parent_path?: string
  service_names: string
  env_branches: Record<string, string>
  _nameManual?: boolean
  _source?: 'local' | 'remote'
  _localPath?: string
  _cloneTarget?: string
  _scanning?: boolean
  _scanError?: string
  _scanned?: boolean
  _serviceEntries?: Record<string, string>
  _submoduleHintsDismissed?: boolean
  _submoduleHints?: { name: string; sub_path: string; stack: string; role: string; reason: string; url?: string }[]
  _submoduleSelection?: Record<string, boolean>
  _roleHint?: { role: string; reason: string }
  _roleHintLoading?: boolean
  _roleManuallyPicked?: boolean
}

interface Environment { id: string }

defineProps<{
  repo: RepoItem
  index: number
  environments: Environment[]
  /** repos.length <= 1 时禁用删除按钮 */
  canRemove: boolean
  /** UI 状态:每行 inline "+" 输入框的值;父端 svcAddInputs[i] 直接绑 v-model */
  svcAddInputs: Record<number, string>
  /** repoBranchesMap[repoName] —— 真实分支列表(没扫到时回退到 input) */
  repoBranchesMap: Record<string, string[]>
  /** 全局 clone 父目录(显示用) */
  reposRootInput: string
  resolvedReposRoot: string
  hasError: (k: string) => boolean
  // helper 函数签名用 any 接收 repo —— 父端 RepoItem 的 role 是窄 union(RepoRole),
  // 这边的 RepoItem 接口为了不重复定义把 role 类型放宽成 string?,两边类型 nominal
  // 不通,函数签名直接匹配会报"两个 RepoItem 不一致"。给 helper 用宽松类型最简洁。
  hasRepoSource: (r: any) => boolean
  displayPath: (p: string) => string
  resolveCloneDest: (r: any) => string
  submodulePathFor: (r: any, sub: string) => string
  isGitSubmodulesHints: (hints: { url?: string }[]) => boolean
  isServiceRole: (role?: any) => boolean
  qualifyServiceName: (repoName: string, hintName: string) => string
  repoServiceNamesList: (r: any) => string[]
  branchHasOptions: (r: any) => boolean
  branchOptionsFor: (r: any, current: string) => string[]
  pickedSubmoduleCount: (r: any) => number
}>()

// emit 签名也用 any 接 r —— 父端 RepoItem 的 role 是 RepoRole 窄 union,跟本组件
// 接口的 string? 不通,直接传具体类型会让父端 @handler 报参数不兼容。
const emit = defineEmits<{
  remove: [idx: number]
  setSource: [r: any, source: 'local' | 'remote']
  urlInput: [r: any]
  nameInput: [r: any]
  subPathInput: [r: any]
  pickCloneTarget: [r: any]
  pickLocalRepoDir: [r: any]
  scanSingleRepo: [r: any]
  toggleSubmodulePick: [r: any, subPath: string, checked: boolean]
  splitMonorepo: [idx: number]
  mergeMonorepoIntoServices: [idx: number]
  syncServiceNamesWithRole: [r: any]
  applyRoleHint: [r: any]
  removeServiceName: [r: any, svc: string]
  addServiceName: [r: any, idx: number]
}>()
</script>

<template>
  <div class="repo-block">
    <div class="repo-header">
      <span class="repo-badge">仓库 {{ index + 1 }}</span>
      <span v-if="repo.sub_path && repo.sub_path.trim()" class="submodule-tag" :title="`子目录: ${repo.sub_path}`">
        📂 {{ repo.sub_path.trim() }}
      </span>
      <span
        v-if="repo.parent_repo && repo.parent_repo.trim()"
        class="submodule-tag umbrella-tag"
      >
        🌂 属于 {{ repo.parent_repo.trim() }} @ {{ repo.parent_path || repo.name || '<name>' }}
        <button
          type="button"
          class="btn-link cc-delete has-hint"
          data-hint="解除关联"
          @click="repo.parent_repo = ''; repo.parent_path = ''"
        >🗑</button>
      </span>
      <button class="btn-icon remove" :disabled="!canRemove" @click="emit('remove', index)">&times;</button>
    </div>

    <!-- 来源切换 -->
    <div class="form-group">
      <label>仓库来源</label>
      <div class="source-toggle">
        <label class="source-option" :class="{ selected: repo._source === 'remote' }">
          <input
            type="radio"
            :checked="repo._source === 'remote'"
            @change="emit('setSource', repo, 'remote')"
          />
          <span class="source-title">🌐 远程 URL</span>
          <span class="source-hint">填 git URL,扫描时 clone 到本地</span>
        </label>
        <label class="source-option" :class="{ selected: repo._source === 'local' }">
          <input
            type="radio"
            :checked="repo._source === 'local'"
            @change="emit('setSource', repo, 'local')"
          />
          <span class="source-title">📁 本地已有</span>
          <span class="source-hint">选一个已 clone 好的仓库目录</span>
        </label>
      </div>
    </div>

    <!-- 远程模式 -->
    <template v-if="repo._source === 'remote'">
      <div class="form-group">
        <label>仓库地址 <span class="required">*</span>
          <span class="field-hint">— 仓库名从 URL 自动推;扫描前需要 clone 到本地</span>
        </label>
        <input
          v-model="repo.url"
          type="text"
          placeholder="git@github.com:org/order-service.git"
          :class="{ error: hasError(`repo.${index}.url`) }"
          @input="emit('urlInput', repo)"
        />
      </div>
      <div class="form-group">
        <label>
          Clone 父目录
          <span class="field-hint">— 选填,不填用全局默认。git clone 会建 <code>/{{ repo.name || '&lt;repo.name&gt;' }}</code> 子目录</span>
        </label>
        <div class="path-input-row">
          <input
            :value="repo._cloneTarget ? displayPath(resolveCloneDest(repo)) : ''"
            type="text"
            :placeholder="`点选父目录(如 ${displayPath(reposRootInput.trim() || resolvedReposRoot)}),会自动建 /${repo.name || '<repo.name>'}`"
            readonly
            class="path-readonly"
            :title="repo._cloneTarget ? `父目录: ${repo._cloneTarget}\n实际仓库: ${resolveCloneDest(repo)}` : ''"
          />
          <button type="button" class="btn" @click="emit('pickCloneTarget', repo)">
            {{ repo._cloneTarget ? '重新选…' : '选目录…' }}
          </button>
          <button
            v-if="repo._cloneTarget"
            type="button"
            class="btn-link cc-delete"
            title="清空自定义目标,回到默认目录"
            @click="repo._cloneTarget = ''"
          >🗑</button>
        </div>
      </div>
      <div class="form-group repo-sync-row">
        <button
          type="button"
          class="btn primary"
          :disabled="!repo.url.trim() || repo._scanning"
          @click="emit('scanSingleRepo', repo)"
        >
          <span v-if="repo._scanning" class="scan-spinner" aria-hidden="true"></span>
          {{ repo._scanning ? 'Clone + 扫描中…' : (repo._scanned ? '🔄 重新同步扫描' : '🔄 同步到本地并扫描') }}
        </button>
        <span v-if="repo._scanning" class="analyze-progress-inline">
          <span class="scan-spinner-mini"></span>
          <span>正在 git clone + DetectStack/Framework + 读取分支列表…</span>
        </span>
        <span v-else-if="repo._scanError" class="repo-scan-error">✗ {{ repo._scanError }}</span>
        <span v-else-if="repo._scanned" class="repo-scan-ok">✓ 已扫描,结果见下方</span>
      </div>
    </template>

    <!-- 本地模式 -->
    <template v-else>
      <div class="form-group">
        <label>本地仓库目录 <span class="required">*</span>
          <span class="field-hint">— 点"选目录"挑一个已 clone 的目录,自动反填 URL + 推仓库名 + 扫描</span>
        </label>
        <div class="path-input-row">
          <input
            :value="repo._localPath"
            type="text"
            placeholder="尚未选择目录"
            readonly
            class="path-readonly"
            :title="repo._localPath || ''"
          />
          <button type="button" class="btn" @click="emit('pickLocalRepoDir', repo)">
            {{ repo._localPath ? '重新选目录…' : '选目录…' }}
          </button>
        </div>
        <div v-if="repo._localPath && repo.url" class="local-url-probe ok">
          ✓ 已识别 origin: <code>{{ repo.url }}</code>
        </div>
        <div v-else-if="repo._localPath && !repo.url" class="local-url-probe warn">
          ⚠ 没读到 <code>git remote origin</code>;yaml 里会用占位 URL(仓库已在本地,不影响扫描)
        </div>
      </div>
      <div v-if="repo._localPath" class="form-group repo-sync-row">
        <button
          type="button"
          class="btn"
          :disabled="repo._scanning"
          @click="emit('scanSingleRepo', repo)"
        >
          <span v-if="repo._scanning" class="scan-spinner-mini" aria-hidden="true"></span>
          {{ repo._scanning ? '扫描中…' : '🔄 重新扫描' }}
        </button>
        <span v-if="repo._scanning" class="analyze-progress-inline">
          <span>DetectStack / Framework + 读取分支…</span>
        </span>
        <span v-else-if="repo._scanError" class="repo-scan-error">✗ {{ repo._scanError }}</span>
        <span v-else-if="repo._scanned" class="repo-scan-ok">✓ 已扫描</span>
      </div>
    </template>

    <!-- 仓库名 + 自动识别 -->
    <div v-if="hasRepoSource(repo)" class="form-group">
      <label>
        仓库名
        <span v-if="!repo._nameManual" class="auto-tag">
          {{ repo._source === 'local' ? '自动从目录名推' : '自动从 URL 推' }}
        </span>
        <span v-else class="field-hint">(已手改;清空可回到自动推)</span>
      </label>
      <input
        v-model="repo.name"
        type="text"
        :placeholder="repo._source === 'local' ? '自动从目录名推出' : '自动从仓库地址推出'"
        :class="{ error: hasError(`repo.${index}.name`) }"
        @input="emit('nameInput', repo)"
      />
    </div>

    <!-- 技术栈展示(readonly) -->
    <div v-if="hasRepoSource(repo)" class="form-group">
      <label>
        技术栈
        <span class="field-hint">(扫描后自动填,只读)</span>
      </label>
      <div v-if="repo._source === 'remote' && repo.url.trim() && !repo._scanned && !repo._scanning" class="auto-scan-hint">
        ⚠ 还没扫描 —
        <strong>点上方"🔄 同步到本地并扫描"按钮</strong>触发
      </div>
      <div class="stack-display" :class="{ empty: !repo.stack }">
        <span v-if="repo._scanning" class="auto-scanning">
          <span class="scan-spinner-mini"></span>扫描中…
        </span>
        <span v-else>{{ repo.stack || '—' }}</span>
      </div>
    </div>

    <RepoMonorepoBanner
      v-if="hasRepoSource(repo)"
      :repo="repo"
      :index="index"
      :is-git-submodules-hints="isGitSubmodulesHints"
      :qualify-service-name="qualifyServiceName"
      :submodule-path-for="submodulePathFor"
      :picked-submodule-count="pickedSubmoduleCount"
      @toggle-submodule-pick="(r, sub, checked) => emit('toggleSubmodulePick', r, sub, checked)"
      @split-monorepo="(idx) => emit('splitMonorepo', idx)"
      @merge-monorepo-into-services="(idx) => emit('mergeMonorepoIntoServices', idx)"
    />

    <!-- 已合并 service_entries 展示 -->
    <div
      v-if="repo._serviceEntries && Object.keys(repo._serviceEntries).length > 0"
      class="service-entries-display"
    >
      <div class="service-entries-head">
        ✓ 本仓 {{ Object.keys(repo._serviceEntries).length }} 个服务的入口路径:
        <button type="button" class="btn-link" @click="repo._submoduleHintsDismissed = false">改</button>
      </div>
      <ul class="service-entries-list">
        <li v-for="(entry, name) in repo._serviceEntries" :key="name">
          <strong>{{ name }}</strong> → <code>{{ entry }}</code>
        </li>
      </ul>
    </div>

    <!-- 已 split 的子模块行 -->
    <div
      v-if="hasRepoSource(repo) && repo.sub_path && repo.sub_path.trim() && repo._localPath"
      class="submodule-path-display"
    >
      <span class="field-hint">本服务源码实际位置:</span>
      <code>{{ submodulePathFor(repo, repo.sub_path) }}</code>
    </div>

    <!-- sub_path 编辑 -->
    <div v-if="hasRepoSource(repo) && repo.sub_path && repo.sub_path.trim()" class="form-group form-group-subpath">
      <label>
        子目录
        <span class="field-hint">(monorepo 子模块路径,跟 url 一起决定本服务源码位置)</span>
      </label>
      <input
        v-model="repo.sub_path"
        type="text"
        class="subpath-input"
        placeholder="services/commerce"
        @input="emit('subPathInput', repo)"
      />
    </div>

    <!-- 角色 -->
    <div v-if="hasRepoSource(repo)" class="form-group">
      <label>
        角色
        <span class="field-hint">(影响 AI 排障时的依赖图分析方向)</span>
      </label>
      <select
        v-model="repo.role"
        class="role-select"
        @change="(repo._roleManuallyPicked = true, emit('syncServiceNamesWithRole', repo))"
      >
        <option value="backend">后端服务 (backend) — 业务微服务,双向依赖图</option>
        <option value="frontend">前端 (frontend) — web app,只调下游不被调</option>
        <option value="gateway">网关 / BFF (gateway) — API 聚合层</option>
        <option value="middleware">中间层 (middleware) — worker / 调度器 / 接入层</option>
        <option value="mobile">移动端 (mobile) — iOS / Android</option>
        <option value="admin">管理后台 (admin) — 内部运营系统</option>
        <option value="common-lib">公共库 (common-lib) — 不入服务图,仅作版本对比</option>
        <option value="infra">基础设施 (infra) — k8s manifest / terraform</option>
        <option value="docs">文档 (docs) — 仅作背景资料</option>
      </select>
      <div v-if="repo._roleHintLoading" class="role-hint-loading">📍 推荐分析中…</div>
      <div
        v-else-if="repo._roleHint"
        class="role-hint"
        :class="{ matched: repo._roleHint.role === repo.role }"
      >
        <span v-if="repo._roleHint.role === repo.role" class="role-hint-icon">✓</span>
        <span v-else class="role-hint-icon">📍</span>
        <span class="role-hint-text">
          <template v-if="repo._roleHint.role === repo.role">推荐:{{ repo._roleHint.role }}</template>
          <template v-else>建议:<strong>{{ repo._roleHint.role }}</strong></template>
          <span class="role-hint-reason">— {{ repo._roleHint.reason }}</span>
        </span>
        <button
          v-if="repo._roleHint.role !== repo.role"
          type="button"
          class="role-hint-apply"
          @click="emit('applyRoleHint', repo)"
        >采用</button>
      </div>
    </div>

    <RepoServiceChips
      v-if="hasRepoSource(repo) && isServiceRole(repo.role)"
      :repo="repo"
      :index="index"
      :service-names="repoServiceNamesList(repo)"
      :svc-add-inputs="svcAddInputs"
      @remove-service-name="(r, svc) => emit('removeServiceName', r, svc)"
      @add-service-name="(r, idx) => emit('addServiceName', r, idx)"
    />

    <RepoEnvBranches
      v-if="hasRepoSource(repo)"
      :repo="repo"
      :environments="environments"
      :repo-branches-map="repoBranchesMap"
      :branch-has-options="branchHasOptions"
      :branch-options-for="branchOptionsFor"
    />
  </div>
</template>

<style scoped>
/* 自画 tooltip(替代 native title 在 Wails WebKit 下 macOS 系统级深色样式看不清的问题)。
 * 任意元素加 class="has-hint" + data-hint="..." 即可,hover 后在元素下方显示明色 tooltip。
 * 跟 RepoListItem 内部组件 scoped,不污染全局。 */
.has-hint {
  position: relative;
}
.has-hint::after {
  content: attr(data-hint);
  position: absolute;
  left: 50%;
  bottom: calc(100% + 6px);
  transform: translateX(-50%);
  padding: 6px 10px;
  background: #2b3344;
  color: #f3f5f9;
  font-size: 12px;
  line-height: 1.4;
  white-space: nowrap;
  border-radius: 4px;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.25);
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.12s ease-out 0.25s;
  z-index: 1000;
}
.has-hint:hover::after {
  opacity: 1;
}
</style>
