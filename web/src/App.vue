<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '@/api/client'

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

// 紧急通道状态指示（relay 在线时显示）
const relayEnabled = ref(false)
const relayOnline = ref(false)
let timer: ReturnType<typeof setInterval> | undefined

async function refreshRelayState() {
  if (!localStorage.getItem('ecp_token')) return
  try {
    const cfg = await api.getRelayConfig()
    relayEnabled.value = !!cfg.enabled
    if (cfg.enabled) {
      // 触发连接（若未连）
      const { relay } = await import('@/api/relay')
      relay.onStatusChange = () => {
        relayOnline.value = relay.isOnline()
      }
      relay.setConfig(cfg)
      relayOnline.value = relay.isOnline()
    }
  } catch {
    relayEnabled.value = false
  }
}

onMounted(() => {
  if (localStorage.getItem('ecp_token')) refreshRelayState()
  timer = setInterval(refreshRelayState, 30000)
})
onUnmounted(() => clearInterval(timer))

// —— 修改密码 ——
async function openChangePassword() {
  const { ElMessageBox, ElMessage } = await import('element-plus')
  const { api } = await import('@/api/client')
  let oldPwd = ''
  try {
    const r1 = await ElMessageBox.prompt('请输入当前密码', '修改密码', {
      inputType: 'password',
      inputPlaceholder: '当前密码',
      confirmButtonText: '下一步',
    })
    oldPwd = r1.value
  } catch {
    return
  }
  let newPwd = ''
  try {
    const r2 = await ElMessageBox.prompt('新密码（6-64 位）', '修改密码', {
      inputType: 'password',
      inputPlaceholder: '新密码',
      confirmButtonText: '下一步',
      inputValidator: (v: string) => (v && v.length >= 6 && v.length <= 64 ? true : '新密码 6-64 位'),
    })
    newPwd = r2.value
  } catch {
    return
  }
  try {
    await ElMessageBox.prompt('再次输入新密码确认', '修改密码', {
      inputType: 'password',
      inputPlaceholder: '确认新密码',
      confirmButtonText: '确认修改',
      inputValidator: (v: string) => (v === newPwd ? true : '两次输入不一致'),
    })
  } catch {
    return
  }
  try {
    await api.changePassword(oldPwd, newPwd)
    ElMessage.success('密码已修改')
  } catch (e: any) {
    ElMessage.error(`修改失败：${e?.message || ''}`)
  }
}
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

      <div class="sidebar-foot">
        <el-button size="small" text style="width: 100%; justify-content: flex-start" @click="openChangePassword">
          <el-icon style="margin-right: 6px"><Lock /></el-icon>修改密码
        </el-button>
      </div>

      <div class="sidebar-footer">
        <div>Tailscale 自组网</div>
        <div class="mono">immml@ · 已连接</div>
        <div v-if="relayEnabled" class="relay-status" :class="{ online: relayOnline }">
          <span class="relay-dot" />
          紧急通道 {{ relayOnline ? '在线' : '连接中…' }}
        </div>
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
