<script setup lang="ts">
import { computed } from 'vue'

interface FrontendEntry {
  name: string
  url: string
  repo: string
}

interface Environment {
  id: string
  frontend_entries: FrontendEntry[]
}

interface Repo {
  name: string
  role?: string
}

const props = defineProps<{
  environments: Environment[]
  repos: Repo[]
}>()

const bindings = computed(() => props.environments.flatMap((env, envIndex) =>
  (env.frontend_entries || []).map((entry, entryIndex) => ({ env, envIndex, entry, entryIndex })),
))

const rolePriority: Record<string, number> = { frontend: 0, admin: 1, mobile: 2 }
const repoOptions = computed(() => props.repos
  .filter(repo => repo.name.trim())
  .slice()
  .sort((a, b) => (rolePriority[a.role || ''] ?? 9) - (rolePriority[b.role || ''] ?? 9)))

const roleLabels: Record<string, string> = {
  frontend: '前端',
  admin: '管理后台',
  mobile: '移动端',
  backend: '后端',
  gateway: '网关 / BFF',
  middleware: '中间件',
  'common-lib': '公共库',
  infra: '基础设施',
  docs: '文档',
}
</script>

<template>
  <section v-if="bindings.length" class="frontend-repo-bindings" data-test="frontend-repo-bindings">
    <div class="binding-heading">
      <div>
        <h3>前端入口对应哪个代码仓库</h3>
        <p>仓库配置完成后在这里关联。机器人会用它定位前端源码；暂时不确定可以先留空。</p>
      </div>
      <span class="binding-count">{{ bindings.length }} 个入口</span>
    </div>

    <div class="binding-list">
      <label
        v-for="binding in bindings"
        :key="`${binding.envIndex}:${binding.entryIndex}`"
        class="binding-row"
      >
        <span class="binding-identity">
          <strong>{{ binding.entry.name || '未命名前端' }}</strong>
          <small>{{ binding.env.id || `环境 ${binding.envIndex + 1}` }} · {{ binding.entry.url || '未填写 URL' }}</small>
        </span>
        <select v-model="binding.entry.repo" :aria-label="`${binding.entry.name || '前端入口'}对应的代码仓库`">
          <option value="">暂不绑定 / 后续识别</option>
          <option v-for="repo in repoOptions" :key="repo.name" :value="repo.name">
            {{ repo.name }}{{ repo.role ? `（${roleLabels[repo.role] || repo.role}）` : '' }}
          </option>
        </select>
      </label>
    </div>
  </section>
</template>

<style scoped>
.frontend-repo-bindings {
  margin: 18px 0;
  padding: 16px;
  border: 1px solid var(--c-line);
  border-radius: 10px;
  background: var(--c-surf-2);
}
.binding-heading {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  align-items: flex-start;
  margin-bottom: 12px;
}
.binding-heading h3 { margin: 0 0 5px; font-size: 15px; }
.binding-heading p { margin: 0; color: var(--c-text-3); font-size: 13px; }
.binding-count {
  flex: none;
  padding: 3px 9px;
  border-radius: 999px;
  color: var(--c-primary);
  background: var(--c-surf-3);
  font-size: 12px;
  font-weight: 650;
}
.binding-list { display: grid; gap: 10px; }
.binding-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(260px, 0.8fr);
  gap: 16px;
  align-items: center;
  padding: 12px;
  border: 1px solid var(--c-line);
  border-radius: 8px;
  background: var(--c-surf);
}
.binding-identity { min-width: 0; display: grid; gap: 4px; }
.binding-identity strong,
.binding-identity small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.binding-identity small { color: var(--c-text-3); }
.binding-row select { width: 100%; }
@media (max-width: 760px) {
  .binding-row { grid-template-columns: 1fr; gap: 9px; }
}
</style>
