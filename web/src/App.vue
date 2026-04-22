<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import ToastContainer from './components/ToastContainer.vue'
// Vite URL import:assets/app-icon.svg 会被打进 bundle,<img src> 直接用。
// 用 app-icon(方形,1024×1024 viewBox) 而不是 logo.svg(宽 780×220)当侧边栏品牌
// 标记——侧边栏宽 220px,方形 icon 挤一下更合适。
import brandIcon from './assets/app-icon.svg'

const route = useRoute()
const currentPath = computed(() => route.path)

// 侧边栏分主路径 + 诊断工具两档。诊断工具(YAML 调试器 / 仓库分析)放主路径
// 后面让新用户视线先扫过去,不进诊断也无感。路径本身没有视觉分组,只是顺序靠后;
// 将来需要的话再加分隔线。
const navItems = [
  { path: '/', icon: '🏠', label: '首页', desc: '概览 + 下一步推荐' },
  { path: '/bots', icon: '🤖', label: '已装机器人', desc: '扫描本机已部署的机器人' },
  { path: '/init', icon: '🧙', label: '创建向导', desc: '生成 system.yaml' },
  // ── 诊断工具(下面两项) ──
  { path: '/editor', icon: '📝', label: 'YAML 调试器', desc: '粘贴 yaml 验证语法 + 干跑 plan' },
  { path: '/analyze', icon: '🔍', label: '仓库分析', desc: '扫代码抽 service_names / 配置中心' },
]
</script>

<template>
  <div class="layout">
    <aside class="sidebar">
      <div class="sidebar-header">
        <img :src="brandIcon" class="sidebar-logo" alt="Troubleshooter Studio" />
        <div class="sidebar-title">AI 排障机器人工作台</div>
        <div class="sidebar-subtitle">troubleshooter-studio</div>
      </div>
      <nav>
        <router-link
          v-for="(item, i) in navItems"
          :key="item.path"
          :to="item.path"
          class="nav-link"
          :class="{ active: currentPath === item.path }"
        >
          <span class="nav-icon">{{ item.icon }}</span>
          <span class="nav-text">
            <span class="nav-label">{{ item.label }}</span>
            <span class="nav-desc">{{ item.desc }}</span>
          </span>
          <span v-if="i === 2" class="nav-badge">推荐</span>
        </router-link>
      </nav>
      <div class="sidebar-footer">
        <div class="sidebar-tip">💡 新用户？从「创建向导」开始</div>
      </div>
    </aside>
    <main class="content">
      <router-view v-slot="{ Component }">
        <keep-alive>
          <component :is="Component" />
        </keep-alive>
      </router-view>
    </main>
    <!-- 全局 toast,右上角浮窗,按需 import { toast } from '@/lib/toast' 推 -->
    <ToastContainer />
  </div>
</template>

<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
html, body, #app { height: 100%; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'PingFang SC', 'Microsoft YaHei', sans-serif; }

.layout { display: flex; height: 100%; }

.sidebar {
  width: 220px; min-width: 220px; background: #1e293b; color: #e2e8f0;
  display: flex; flex-direction: column; padding: 0;
}
.sidebar-header {
  padding: 20px 18px 14px; border-bottom: 1px solid #334155;
  display: flex; flex-direction: column; align-items: flex-start; gap: 4px;
}
.sidebar-logo {
  width: 44px; height: 44px; border-radius: 10px; margin-bottom: 4px;
  /* svg 里有深色 backdrop;侧边栏也是深色,靠阴影跟底色拉开 */
  box-shadow: 0 2px 8px rgba(59,130,246,0.25), 0 0 0 1px rgba(148,163,184,0.12);
}
.sidebar-title { font-size: 15px; font-weight: 700; color: #f8fafc; line-height: 1.3; }
.sidebar-subtitle { font-size: 11px; color: #64748b; font-family: monospace; }

nav { display: flex; flex-direction: column; padding: 8px 0; flex: 1; }

.nav-link {
  display: flex; align-items: center; gap: 10px;
  padding: 10px 16px; color: #94a3b8; text-decoration: none; font-size: 13px;
  transition: background 0.15s, color 0.15s; position: relative;
}
.nav-link:hover { background: #334155; color: #e2e8f0; }
.nav-link.active { background: #3b82f6; color: #fff; }
.nav-link.active .nav-desc { color: rgba(255,255,255,0.7); }

.nav-icon { font-size: 16px; flex-shrink: 0; width: 22px; text-align: center; }
.nav-text { display: flex; flex-direction: column; }
.nav-label { font-weight: 600; font-size: 13px; }
.nav-desc { font-size: 10px; color: #64748b; margin-top: 1px; }
.nav-badge {
  position: absolute; right: 12px; top: 50%; transform: translateY(-50%);
  background: #f59e0b; color: #1e293b; font-size: 9px; font-weight: 700;
  padding: 1px 6px; border-radius: 8px;
}

.sidebar-footer {
  padding: 12px 16px; border-top: 1px solid #334155;
}
.sidebar-tip { font-size: 11px; color: #64748b; }

.content { flex: 1; background: #fff; padding: 32px; overflow-y: auto; }
</style>
