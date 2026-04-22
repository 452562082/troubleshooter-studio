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
      // 嵌入式 chat:iframe 指向 standalone server.py 的 localhost:<port>。
      // 路径名 "/bots/chat" + query.path=<产物绝对目录>,reload/书签也能恢复 —— 只要对应 runner 还活着。
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
    {
      path: '/plan',
      name: 'Plan',
      component: () => import('../pages/PlanPage.vue'),
    },
    {
      path: '/gen',
      name: 'Gen',
      component: () => import('../pages/GenPage.vue'),
    },
    {
      path: '/doctor',
      name: 'Doctor',
      component: () => import('../pages/DoctorPage.vue'),
    },
    {
      path: '/diff',
      name: 'Diff',
      component: () => import('../pages/DiffPage.vue'),
    },
  ],
})

export default router
