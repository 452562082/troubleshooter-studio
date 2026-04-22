import { createRouter, createWebHistory } from 'vue-router'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/',
      name: 'Home',
      component: () => import('../pages/HomePage.vue'),
    },
    {
      path: '/bots',
      name: 'Bots',
      component: () => import('../pages/BotsPage.vue'),
    },
    {
      // 嵌入式 chat:Studio 原生(直连 LLM API,见 internal/llmchat)。
      // 路径名 "/bots/chat" + query.path=<产物绝对目录>,reload/书签也能恢复。
      path: '/bots/chat',
      name: 'BotsChat',
      component: () => import('../pages/BotsChat.vue'),
    },
    {
      path: '/init',
      name: 'Init',
      component: () => import('../pages/InitPage.vue'),
    },
    {
      path: '/editor',
      name: 'Editor',
      component: () => import('../pages/EditorPage.vue'),
    },
    {
      path: '/analyze',
      name: 'Analyze',
      component: () => import('../pages/AnalyzePage.vue'),
    },
  ],
})

export default router
