<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()

const navItems = [
  { path: '/nodes', title: '节点', icon: 'Monitor' },
  { path: '/network', title: '组网', icon: 'Connection' },
  { path: '/alerts', title: '告警', icon: 'Bell' },
  { path: '/audit', title: '审计日志', icon: 'Document' },
]

const title = computed(() => (route.meta.title as string) ?? '控制台')
const activePath = computed(() => {
  const p = route.path
  // 详情页与终端页都应高亮"节点"
  if (p.startsWith('/nodes')) return '/nodes'
  if (p.startsWith('/network')) return '/network'
  if (p.startsWith('/alerts')) return '/alerts'
  if (p.startsWith('/audit')) return '/audit'
  return p
})
</script>

<template>
  <div class="layout">
    <aside class="sidebar">
      <div class="sidebar-brand">
        <span class="brand-dot" />
        边缘节点控制台
      </div>

      <nav class="sidebar-nav">
        <router-link
          v-for="item in navItems"
          :key="item.path"
          :to="item.path"
          class="nav-item"
          :class="{ 'is-active': activePath === item.path }"
        >
          <el-icon><component :is="item.icon" /></el-icon>
          <span>{{ item.title }}</span>
        </router-link>
      </nav>

      <div class="sidebar-footer">
        <div>Tailscale 自组网</div>
        <div class="mono">immml@ · 已连接</div>
      </div>
    </aside>

    <main class="main">
      <header class="page-header">
        <div class="page-title">{{ title }}</div>
        <div class="text-secondary" style="font-size: 13px">
          控制面按需上线 · 节点常在线自治
        </div>
      </header>
      <div class="page-body">
        <router-view />
      </div>
    </main>
  </div>
</template>
