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
      path: '/bugs',
      name: 'Bugs',
      component: () => import('../pages/BugInboxPage.vue'),
    },
    {
      path: '/incidents',
      name: 'Incidents',
      component: () => import('../pages/IncidentWorkbenchPage.vue'),
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
      path: '/logs',
      name: 'Logs',
      component: () => import('../pages/LogsPage.vue'),
    },
  ],
})

export default router
