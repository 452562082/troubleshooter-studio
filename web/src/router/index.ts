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
