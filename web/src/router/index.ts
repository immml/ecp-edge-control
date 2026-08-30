import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { api } from '@/api/client'

const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/nodes' },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { title: '登录', hidden: true },
  },
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
    path: '/nodes/:id/containers',
    name: 'containers',
    component: () => import('@/views/ContainersView.vue'),
    meta: { title: '容器', hidden: true },
  },
  {
    path: '/nodes/:id/vnc',
    name: 'vnc',
    component: () => import('@/views/VncView.vue'),
    meta: { title: 'VNC', hidden: true },
  },
  {
    path: '/audit',
    name: 'audit',
    component: () => import('@/views/AuditView.vue'),
    meta: { title: '审计日志', icon: 'Document' },
  },
  {
    path: '/network',
    name: 'network',
    component: () => import('@/views/NetworkView.vue'),
    meta: { title: '组网管理', icon: 'Connection' },
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

// 登录守卫：未携带 token 必须先登录；已登录访问 /login 直接进控制台
router.beforeEach((to) => {
  const authed = api.isAuthed()
  if (!authed && to.path !== '/login') return '/login'
  if (authed && to.path === '/login') return '/nodes'
  return true
})

router.afterEach((to) => {
  const title = to.meta.title as string | undefined
  document.title = title ? `${title} · 边缘节点控制台` : '边缘节点控制台'
})

export default router
