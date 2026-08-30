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
let vncPassword = ''

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
  // 询问用户设置 VNC 密码（连接远程桌面时使用）
  const res = await ElMessageBox.prompt(
    '设置 VNC 访问密码，连接远程桌面时需输入此密码（VNC 协议限制最长 8 位）。',
    '设置 VNC 密码',
    {
      inputType: 'password',
      inputPlaceholder: '输入密码（4-8 位）',
      inputValidator: (v: string) => {
        const pwd = v || ''
        if (pwd.trim().length < 4 || pwd.length > 8) return '密码 4-8 位'
        if (!/^[A-Za-z0-9!@#$%^&*._-]+$/.test(pwd)) return '仅限字母数字和 !@#$%^&*._-'
        return true
      },
      confirmButtonText: '设置并启动',
    },
  ).catch(() => null)
  if (!res?.value) return
  vncPassword = res.value as string
  await doVncStart(vncPassword, '')
}

// doVncStart 执行 vnc_start；sudoPwd 为空则自动弹框询问（自动配置模式）
async function doVncStart(password: string, sudoPwd: string) {
  const params: Record<string, unknown> = { password }
  if (sudoPwd) params.sudo_password = sudoPwd
  const r = await api.execCommand(nodeId, 'vnc_start', params)
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
    // 优先自动配置：询问节点 sudo 密码（仅本次授权使用，不保存）
    const pwdRes = await ElMessageBox.prompt(
      '需要节点 sudo 权限完成自动配置（免密 sudo + 设密码 + 启动）。请输入节点 sudo 密码——仅本次授权使用，不保存。',
      '输入 sudo 密码（自动配置）',
      {
        inputType: 'password',
        inputPlaceholder: '节点 sudo 密码',
        confirmButtonText: '自动配置并启动',
      },
    ).catch(() => null)

    if (pwdRes?.value) {
      const sudoPwd = pwdRes.value as string
      const r2 = await api.execCommand(nodeId, 'vnc_start', { password: vncPassword, sudo_password: sudoPwd })
      if (r2.status === ResultStatus.OK) {
        ElMessage.success('VNC 已启动（自动配置完成，之后全自动）')
        await loadStatus()
        if (vncStatus.value?.running) connect()
        return
      }
      if (r2.status === ResultStatus.FAILED) {
        ElMessage.error(`自动配置失败：${r2.message || 'sudo 密码可能不正确'}`)
        return
      }
      // 仍需要提权（异常）：落入脚本兜底
    }

    // 兜底：显示手动脚本（用户可在节点上 sudo 执行）
    const script = r.privilege_script || ''
    const hint = r.privilege_hint || '需要 root 权限'
    await ElMessageBoxConfirm(
      `<div style="font-size:13px;line-height:1.7">
         <p style="margin:0 0 8px">${hint}</p>
         <pre style="background:var(--el-fill-color-light);padding:10px;border-radius:6px;overflow:auto;font-size:12px;white-space:pre-wrap">${script.replace(/</g, '&lt;')}</pre>
       </div>`,
      `${label}需要提权`,
    )
    await loadStatus()
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
  // VNC 服务端可能监听在 5900/5901...（按 vnc_status 探测的实际端口）
  const port = vncStatus.value?.port || 5900
  const wsUrl = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/v1/nodes/${nodeId}/vnc/ws?token=${encodeURIComponent(api.token || '')}&port=${port}`
  // 直接带上用户设置/输入的密码，避免 noVNC 弹框输错
  rfb = new RFB(container.value, wsUrl, { credentials: { password: vncPassword } })
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
    // 兜底：vncPassword 为空（VNC 已在跑、本次会话未设置）时询问
    ElMessageBox.prompt('请输入 VNC 密码', 'VNC 认证', {
      inputType: 'password',
      confirmButtonText: '连接',
    }).then(({ value }) => {
      vncPassword = value
      rfb?.sendCredentials({ password: value })
    }).catch(() => {})
  })
  rfb.addEventListener('securityfailure', (e: any) => {
    connecting.value = false
    ElMessage.error(`VNC 认证失败：${e?.detail?.reason || ''}。若密码不确定，请先点「停止」再重新「启动」设置新密码。`)
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
