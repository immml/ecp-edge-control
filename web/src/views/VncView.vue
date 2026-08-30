<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Refresh, VideoPlay, VideoPause } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import RFB from '@novnc/novnc'

import { api, ResultStatus, type CommandResult } from '@/api/client'

const route = useRoute()
const router = useRouter()
const nodeId = route.params.id as string

const container = ref<HTMLDivElement>()
let rfb: RFB | null = null
const connected = ref(false)
const connecting = ref(false)
const vncStatus = ref<any>(null)
const statusLoading = ref(false)

// —— VNC server 状态 ——
async function loadStatus() {
  statusLoading.value = true
  try {
    const r = await api.execCommand(nodeId, 'vnc_status', {})
    try { vncStatus.value = JSON.parse(r.stdout || '{}') } catch { vncStatus.value = {} }
  } catch (e: any) {
    ElMessage.error(`VNC 状态查询失败：${e?.message || ''}`)
  } finally {
    statusLoading.value = false
  }
}

async function startVnc() {
  const r = await api.execCommand(nodeId, 'vnc_start', {})
  await handleVncResult(r, '启动 VNC')
  await loadStatus()
  if (vncStatus.value?.running) connect()
}

async function stopVnc() {
  const r = await api.execCommand(nodeId, 'vnc_stop', {})
  await handleVncResult(r, '停止 VNC')
  await loadStatus()
  if (connected.value) disconnect()
}

async function handleVncResult(r: CommandResult, label: string) {
  if (r.status === ResultStatus.NEEDS_PRIVILEGE) {
    const script = r.privilege_script || ''
    const hint = r.privilege_hint || '需要 root 权限（平台不自行提权）'
    const res = await ElMessageBoxConfirm(
      `<div style="font-size:13px;line-height:1.7">
         <p style="margin:0 0 8px">${hint}</p>
         <pre style="background:var(--el-fill-color-light);padding:10px;border-radius:6px;overflow:auto;font-size:12px;white-space:pre-wrap">${script.replace(/</g, '&lt;')}</pre>
       </div>`,
      `${label}需要提权`,
    )
    if (res) await loadStatus()
  } else if (r.status === ResultStatus.OK) {
    ElMessage.success(`${label}成功`)
  } else {
    ElMessage.warning(`${label}：${r.message || '未成功'}`)
  }
}

async function ElMessageBoxConfirm(html: string, title: string): Promise<boolean> {
  try {
    await ElMessageBox.confirm(html, title, {
      dangerouslyUseHTMLString: true,
      confirmButtonText: '我已执行',
      cancelButtonText: '关闭',
      showClose: false,
    })
    return true
  } catch {
    return false
  }
}

// —— noVNC 连接 ——
function connect() {
  if (!container.value) return
  connecting.value = true
  const wsUrl = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/v1/nodes/${nodeId}/vnc/ws?token=${encodeURIComponent(api.token || '')}`
  rfb = new RFB(container.value, wsUrl, { credentials: { password: '' } })
  rfb.scaleViewport = true
  rfb.resizeSession = false
  rfb.addEventListener('connect', () => {
    connected.value = true
    connecting.value = false
    ElMessage.success('VNC 已连接')
  })
  rfb.addEventListener('disconnect', () => {
    connected.value = false
    connecting.value = false
  })
  rfb.addEventListener('credentialsrequired', () => {
    ElMessageBox.prompt('请输入 VNC 密码', 'VNC 认证', {
      inputType: 'password',
      confirmButtonText: '连接',
    }).then(({ value }) => {
      rfb?.sendCredentials({ password: value })
    }).catch(() => {})
  })
  rfb.addEventListener('securityfailure', (e: any) => {
    ElMessage.error(`VNC 认证失败：${e?.detail?.reason || ''}`)
  })
}

function disconnect() {
  if (rfb) {
    rfb.disconnect()
    rfb = null
  }
  connected.value = false
}

onMounted(() => {
  loadStatus()
})
onBeforeUnmount(() => disconnect())
</script>

<template>
  <div style="display: flex; flex-direction: column; gap: 10px; padding: 16px">
    <div style="display: flex; align-items: center; gap: 10px">
      <el-button size="small" text @click="router.push(`/nodes/${nodeId}`)">← 返回</el-button>
      <span style="font-weight: 600">VNC 远程桌面</span>
      <span class="text-secondary" style="font-size: 12.5px">{{ nodeId }}</span>
      <span style="flex: 1"></span>
      <el-tag v-if="vncStatus" size="small" :type="vncStatus.running ? 'success' : 'info'">
        VNC {{ vncStatus.running ? `运行中 :${vncStatus.port}` : '未运行' }}
      </el-tag>
      <el-button size="small" :icon="VideoPlay" :loading="statusLoading" @click="startVnc">启动</el-button>
      <el-button size="small" :icon="VideoPause" :loading="statusLoading" @click="stopVnc">停止</el-button>
      <el-button size="small" :icon="Refresh" @click="loadStatus">刷新</el-button>
    </div>

    <el-alert
      v-if="vncStatus && !vncStatus.installed"
      type="error"
      :closable="false"
      show-icon
      title="节点未安装 VNC"
      :description="vncStatus.hint || '请安装 tigervnc-standalone-server 后重试'"
    />
    <el-alert
      v-else-if="vncStatus && !vncStatus.running"
      type="info"
      :closable="false"
      show-icon
      :title="vncStatus.password ? 'VNC 未运行，点击「启动」' : 'VNC 密码未设置'"
      :description="vncStatus.password ? '启动后即可通过下方画布连接' : '首次启动需在节点执行 vncpasswd 设置密码（会弹出可复制脚本）'"
    />

    <div
      v-show="vncStatus?.running || connected"
      style="position: relative; width: 100%; height: calc(100vh - 160px); background: #111; border-radius: 6px; overflow: hidden"
    >
      <div v-if="connecting" style="position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: #ccc; font-size: 13px">
        连接中…
      </div>
      <div ref="container" style="width: 100%; height: 100%" />
    </div>

    <div v-show="vncStatus?.running && !connected && !connecting" class="text-secondary" style="font-size: 13px; text-align: center; padding: 10px">
      VNC 服务已运行，点击画布区域连接。首次会弹出密码框（VNC 密码）。
    </div>
  </div>
</template>
