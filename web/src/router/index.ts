import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/nodes' },
  {
    path: '/nodes',
    name: 'nodes',
    component: () => import('@/views/NodesView.vue'),
    meta: { title: '节点', icon: 'Monitor' },
  },
  {
    path: '/nodes/:id',
    name: 'node-detail',
    component: () => import('@/views/NodeDetailView.vue'),
    meta: { title: '节点详情', hidden: true },
  },
  {
    path: '/nodes/:id/terminal',
    name: 'terminal',
    component: () => import('@/views/TerminalView.vue'),
    meta: { title: '终端', hidden: true },
  },
  {
    path: '/nodes/:id/files',
    name: 'files',
    component: () => import('@/views/FilesView.vue'),
    meta: { title: '文件', hidden: true },
  },
  {
    path: '/audit',
    name: 'audit',
    component: () => import('@/views/AuditView.vue'),
    meta: { title: '审计日志', icon: 'Document' },
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.afterEach((to) => {
  const title = to.meta.title as string | undefined
  document.title = title ? `${title} · 边缘节点控制台` : '边缘节点控制台'
})

export default router
