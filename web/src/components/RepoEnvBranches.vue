<script setup lang="ts">
// RepoEnvBranches —— 仓库的"环境 → 分支" 映射表。
// 扫到分支列表时走 <select>,没扫到时回退 <input> 兜底,父端 repo.env_branches 直接 v-model。

interface Environment { id: string }

defineProps<{
  /** 父端 repo reactive 直传(env_branches 子字段双向绑定) */
  repo: { name: string; env_branches: Record<string, string> }
  environments: Environment[]
  /** repoBranchesMap[repoName] —— 真实分支列表(没扫到时空数组,走 input 兜底) */
  repoBranchesMap: Record<string, string[]>
  branchHasOptions: (r: any) => boolean
  branchOptionsFor: (r: any, current: string) => string[]
}>()
</script>

<template>
  <div class="form-group">
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
</template>
