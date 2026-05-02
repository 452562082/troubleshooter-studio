<script setup lang="ts">
// RepoListItem —— Step 4 仓库列表的单行编辑器(也是 InitPage 单文件组件中最复杂的子组件):
//   - 仓库 head(badge + sub_path 标 + 删除按钮)
//   - 来源切换(remote URL / local 已有目录)
//   - 远程模式:URL 输入 + clone 父目录 + 同步扫描按钮
//   - 本地模式:目录选择 + URL probe 反馈
//   - 仓库名 + 技术栈展示(readonly,扫描自动填)
//   - monorepo banner(0/1/N 子模块)+ 一键拆分 / 合并按钮
//   - 已合并 service_entries 展示
//   - sub_path 编辑(monorepo 子目录)
//   - 角色下拉 + RecommendRoleForRepo 推荐 chip
//   - 服务名 chip 列表 + inline "+" 输入
//   - env_branches 映射(<select> 当扫到分支 / <input> 兜底)
//
// 父端持有 repos / repoBranchesMap / svcAddInputs / reposRootInput / resolvedReposRoot 等 reactive,
// 大量 helper 通过函数 prop 传入;repo 对象 reactive 直传,v-model 写值同步回父端 repos[i]。

interface RepoItem {
  name: string
  url: string
  stack: string
  framework: string
  role?: string
  sub_path?: string
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
          Clone 父目录 <span class="required">*</span>
          <span class="field-hint">
            — 必填:选父目录,git clone 自动建 <code>/{{ repo.name || '&lt;repo.name&gt;' }}</code> 子目录。
            **本地路径不允许走全局默认隐式回落**(产物里 repo-path-map.yaml 必须有显式路径才能跑代码扫描 / 排障)。
          </span>
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

    <!-- monorepo banner(0/1/N 子模块)+ 一键拆分 / 合并 -->
    <div
      v-if="hasRepoSource(repo) && repo._submoduleHints !== undefined && !repo._submoduleHintsDismissed"
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
      <select v-model="repo.role" class="role-select" @change="emit('syncServiceNamesWithRole', repo)">
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

    <!-- 服务名 chips -->
    <div v-if="hasRepoSource(repo) && isServiceRole(repo.role)" class="form-group">
      <label>
        服务名
        <span class="help-icon" title="config-map 以此为 key。扫描会自动识别(monorepo 列所有子模块);识别不全时点 + 手动补,不想要的点 ✕ 删。">?</span>
        <span v-if="repoServiceNamesList(repo).length" class="field-hint">
          — {{ repoServiceNamesList(repo).length }} 个(✕ 删 / + 补)
        </span>
        <span v-else class="field-hint">(扫一下自动填,或点下方 + 手动补)</span>
      </label>
      <div v-if="repo._scanning" class="service-chips-row">
        <span class="auto-scanning"><span class="scan-spinner-mini"></span>扫描中…</span>
      </div>
      <div v-else class="service-chips-row">
        <span
          v-for="svc in repoServiceNamesList(repo)"
          :key="svc"
          class="service-chip"
        >
          <span class="service-chip-name">{{ svc }}</span>
          <button
            type="button"
            class="service-chip-x"
            :title="`删除 ${svc}`"
            @click="emit('removeServiceName', repo, svc)"
          >✕</button>
        </span>
        <span class="service-chip-add">
          <input
            v-model="svcAddInputs[index]"
            type="text"
            :placeholder="repoServiceNamesList(repo).length ? '+ 补一个服务名' : '+ 手填服务名'"
            @keydown.enter.prevent="emit('addServiceName', repo, index)"
          />
          <button
            type="button"
            class="service-chip-add-btn"
            :disabled="!(svcAddInputs[index] || '').trim()"
            title="添加(Enter 也行;逗号/空格分隔可一次加多个)"
            @click="emit('addServiceName', repo, index)"
          >+</button>
        </span>
      </div>
    </div>

    <!-- env_branches map -->
    <div v-if="hasRepoSource(repo)" class="form-group">
      <label>
        环境 → 分支映射
        <span class="help-icon" title="routing skill 根据此映射切到正确代码分支做代码定位。扫描仓库时按 env.id/is_prod 跟真实分支名做启发式匹配(dev→develop, prod→main/master,..),点下拉可改。">?</span>
        <span v-if="repoBranchesMap[repo.name]?.length" class="field-hint">
          — ✓ 从 {{ repoBranchesMap[repo.name]!.length }} 个真实分支里挑(可改)
        </span>
        <span v-else-if="branchHasOptions(repo)" class="field-hint">
          — yaml 里声明的分支(本地未 clone,无法列全量真实分支;clone 完会刷新)
        </span>
        <span v-else class="field-hint">(扫一下自动映射)</span>
      </label>
      <div class="branch-select-grid">
        <div v-for="env in environments" :key="env.id" class="branch-select-item">
          <span class="branch-env">{{ env.id || '?' }}</span>
          <span class="branch-arrow">→</span>
          <select
            v-if="branchHasOptions(repo)"
            v-model="repo.env_branches[env.id]"
            class="branch-select"
          >
            <option value="">—</option>
            <option
              v-for="b in branchOptionsFor(repo, repo.env_branches[env.id])"
              :key="b"
              :value="b"
            >{{ b }}</option>
          </select>
          <input
            v-else
            v-model="repo.env_branches[env.id]"
            type="text"
            class="branch-input"
            placeholder="扫一下自动填,也可手填"
          />
        </div>
      </div>
    </div>
  </div>
</template>
