<script setup lang="ts">
// 组网面板（节点维度）：Tailscale（登录/停用）+ FRP（多 frpc 实例操作/编辑 ini）。
// 供 NetworkView 与 NodeDetailView 复用：props 传单个节点。
import { onMounted, ref } from 'vue'
import { VideoPlay, VideoPause, Upload } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'

import { api, ResultStatus, type ApiNode, type CommandResult } from '@/api/client'

const props = defineProps<{ node: ApiNode }>()

const loading = ref(false)
const busy = ref<string>('')

interface FrpInstance {
  name: string
  source: string
  unit: string
  running: boolean
  enabled: string
  bin: string
  cmdline: string
  config: string
  configured: boolean
  tunnels?: string[]
}
const tsState = ref<{ loaded: boolean; loggedIn: boolean; summary: string }>({
  loaded: false, loggedIn: false, summary: '—',
})
const frpInstances = ref<FrpInstance[]>([])
const frpLoaded = ref(false)

async function load() {
  if (loading.value) return
  loading.value = true
  tsState.value.loaded = false
  frpLoaded.value = false
  try {
    // Tailscale
    try {
      const r = await api.execCommand(props.node.id, 'tailscale_status', {})
      const text = (r.stdout || '').trim()
      tsState.value.loaded = true
      tsState.value.loggedIn = !!text && !text.startsWith('Log')
      tsState.value.summary = summarizeTs(text)
    } catch {
      tsState.value.loaded = true
      tsState.value.summary = '查询失败'
      tsState.value.loggedIn = false
    }
    // FRP
    try {
      const r = await api.execCommand(props.node.id, 'frp_status', {})
      try {
        frpInstances.value = JSON.parse(r.stdout || '[]') || []
      } catch {
        frpInstances.value = []
      }
      frpLoaded.value = true
    } catch {
      frpLoaded.value = true
    }
  } finally {
    loading.value = false
  }
}

function summarizeTs(stdout: string): string {
  const t = (stdout || '').trim()
  if (!t || t.startsWith('Log')) return '未登录'
  return t.split('\n').slice(0, 4).join('\n')
}

// —— Tailscale 登录 ——
async function tailscaleLogin() {
  const key = `ts:login`
  if (busy.value) return
  busy.value = key
  try {
    const r = await api.execCommand(props.node.id, 'tailscale_login_url', {})
    const j = JSON.parse(r.stdout || '{}')
    if (j.logged_in && !j.url) {
      ElMessage.success(`${props.node.hostname || props.node.id} 已登录`)
      await load()
      return
    }
    if (j.url) {
      window.open(j.url, '_blank', 'noopener')
      await ElMessageBox.confirm(
        `<div style="font-size:13px;line-height:1.7">
           <p>已在新标签页打开 Tailscale 登录链接，请在 <b>login.tailscale.com</b> 完成授权后回到这里。</p>
           <p>登录链接：<a href="${j.url}" target="_blank" style="word-break:break-all">${j.url}</a></p>
           <p style="color:var(--el-text-color-secondary);margin-top:8px">完成授权后点「已完成」刷新状态。</p>
         </div>`,
        `${props.node.hostname || props.node.id} · Tailscale 登录`,
        { dangerouslyUseHTMLString: true, confirmButtonText: '已完成，刷新状态', cancelButtonText: '稍后' },
      ).catch(() => null)
      await load()
    } else {
      ElMessage.warning(`未获取到登录链接：${j.raw || r.message || ''}`)
    }
  } catch (e: any) {
    ElMessage.error(`登录失败：${e?.message || ''}`)
  } finally {
    busy.value = ''
  }
}

async function tailscaleDown() {
  await doTailscaleAction('tailscale_down', '停用')
}
async function doTailscaleAction(type: string, label: string) {
  const key = `ts:${type}`
  if (busy.value) return
  busy.value = key
  try {
    const r = await api.execCommand(props.node.id, type, {})
    await handlePrivilegeResult(r, label)
    await load()
  } catch (e: any) {
    ElMessage.error(`${label}失败：${e?.message || ''}`)
  } finally {
    busy.value = ''
  }
}

// —— FRP 启停 ——
async function frpUp(inst: FrpInstance) { await frpAction(inst, 'up') }
async function frpDown(inst: FrpInstance) { await frpAction(inst, 'down') }
async function frpAction(inst: FrpInstance, action: 'up' | 'down') {
  const key = `frp:${inst.name}:${action}`
  if (busy.value) return
  busy.value = key
  try {
    const r = await api.execCommand(props.node.id, `frp_${action}`, { instance: inst.name })
    await handlePrivilegeResult(r, `frpc[${inst.name}] ${action === 'up' ? '启动' : '停止'}`)
    await load()
  } catch (e: any) {
    ElMessage.error(`操作失败：${e?.message || ''}`)
  } finally {
    busy.value = ''
  }
}

// —— FRP 编辑 frpc.ini ——
const editVisible = ref(false)
const editInst = ref<FrpInstance | null>(null)
const editContent = ref('')
const editSaving = ref(false)

async function editFrpcIni(inst: FrpInstance) {
  try {
    const r = await api.execCommand(props.node.id, 'frp_config_get', { instance: inst.name })
    editInst.value = inst
    editContent.value = r.stdout || ''
    editVisible.value = true
  } catch (e: any) {
    ElMessage.error(`读取配置失败：${e?.message || ''}`)
  }
}

async function saveFrpcIni() {
  if (!editInst.value) return
  editSaving.value = true
  try {
    const r = await api.execCommand(props.node.id, 'frp_config_set', {
      instance: editInst.value.name,
      content: editContent.value,
    })
    if (r.status === ResultStatus.NEEDS_PRIVILEGE) {
      await showPrivilegeDialog('保存 frpc.ini', r)
    } else if (r.status === ResultStatus.OK) {
      ElMessage.success('保存成功')
      editVisible.value = false
    } else {
      ElMessage.warning(`保存：${r.message || '未成功'}`)
    }
    await load()
  } catch (e: any) {
    ElMessage.error(`保存失败：${e?.message || ''}`)
  } finally {
    editSaving.value = false
  }
}

function handleUpload(file: File) {
  const reader = new FileReader()
  reader.onload = () => { editContent.value = String(reader.result || '') }
  reader.readAsText(file)
  return false
}

async function handlePrivilegeResult(r: CommandResult, label: string) {
  if (r.status === ResultStatus.NEEDS_PRIVILEGE) {
    await showPrivilegeDialog(label, r)
  } else if (r.status === ResultStatus.OK) {
    ElMessage.success(`${props.node.hostname || props.node.id} · ${label}成功`)
  } else {
    ElMessage.warning(`${props.node.hostname || props.node.id} · ${label}：${r.message || '未成功'}`)
  }
}

async function showPrivilegeDialog(label: string, r: CommandResult) {
  const script = r.privilege_script || ''
  await ElMessageBox.confirm(
    `<div style="font-size:13px;line-height:1.7">
       <p style="margin:0 0 8px">${r.privilege_hint || '该操作需要 root 权限'}（平台不自行提权）。</p>
       <p style="margin:0 0 6px">请在节点 <b>${props.node.hostname || props.node.id}</b> 上执行：</p>
       <pre style="background:var(--el-fill-color-light);padding:10px;border-radius:6px;overflow:auto;font-size:12px;white-space:pre-wrap">${script.replace(/</g, '&lt;')}</pre>
     </div>`,
    `${props.node.hostname || props.node.id} · ${label}需要提权`,
    { dangerouslyUseHTMLString: true, confirmButtonText: '我已执行', cancelButtonText: '关闭', showClose: false },
  ).catch(() => null)
}

onMounted(load)
</script>

<template>
  <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 14px">
    <!-- FRP 左 -->
    <div class="card" style="margin: 0">
      <div class="card-header">
        <span>FRP（多 frpc 实例）</span>
        <el-tag size="small" :type="frpInstances.some((i) => i.running) ? 'success' : 'info'">
          {{ frpInstances.some((i) => i.running) ? '有实例运行' : '未运行' }}
        </el-tag>
      </div>
      <div class="card-body" v-loading="!frpLoaded">
        <el-empty v-if="frpLoaded && !frpInstances.length" description="未发现 frpc 实例" :image-size="40" />
        <div v-for="inst in frpInstances" :key="inst.name" style="border: 1px solid var(--el-border-color-lighter); border-radius: 6px; padding: 8px 10px; margin-bottom: 8px">
          <div style="display: flex; align-items: center; gap: 6px; flex-wrap: wrap">
            <b style="font-size: 13px">{{ inst.name }}</b>
            <el-tag size="small" :type="inst.running ? 'success' : 'info'">{{ inst.running ? '运行中' : '未运行' }}</el-tag>
            <el-tag v-if="inst.enabled === 'enabled'" size="small" type="primary">自启</el-tag>
            <el-tag v-if="inst.configured" size="small">ini: {{ inst.config }}</el-tag>
          </div>
          <div v-if="inst.cmdline" class="mono text-secondary" style="font-size: 11.5px; margin-top: 4px; word-break: break-all">{{ inst.cmdline }}</div>
          <div style="display: flex; gap: 6px; margin-top: 6px; flex-wrap: wrap">
            <el-button size="small" :icon="VideoPlay" :loading="busy === `frp:${inst.name}:up`" @click="frpUp(inst)">启动</el-button>
            <el-button size="small" :icon="VideoPause" :loading="busy === `frp:${inst.name}:down`" @click="frpDown(inst)">停止</el-button>
            <el-button size="small" type="primary" plain @click="editFrpcIni(inst)">编辑 frpc.ini</el-button>
          </div>
          <div v-if="inst.tunnels?.length" class="text-secondary" style="font-size: 11.5px; margin-top: 4px">
            隧道段: {{ inst.tunnels.join(', ') }}
          </div>
        </div>
      </div>
    </div>

    <!-- Tailscale 右 -->
    <div class="card" style="margin: 0">
      <div class="card-header">
        <span>Tailscale（组网 / 登录）</span>
        <el-tag v-if="tsState.loaded" size="small" :type="tsState.loggedIn ? 'success' : 'warning'">
          {{ tsState.loggedIn ? '已登录' : '未登录' }}
        </el-tag>
      </div>
      <div class="card-body" v-loading="!tsState.loaded">
        <pre v-if="tsState.loggedIn" style="white-space: pre-wrap; font-size: 12px; line-height: 1.6; margin: 0; min-height: 60px">{{ tsState.summary }}</pre>
        <div v-else style="min-height: 60px">
          <el-alert type="warning" :closable="false" show-icon title="节点未登录 tailnet" description="点击「登录」在新标签页打开 Tailscale 官方认证页面，完成授权后自动激活。" />
        </div>
        <div style="display: flex; gap: 8px; margin-top: 12px; flex-wrap: wrap">
          <el-button v-if="!tsState.loggedIn" type="primary" :loading="busy === 'ts:login'" @click="tailscaleLogin">登录</el-button>
          <el-button v-if="tsState.loggedIn" type="warning" :loading="busy === 'ts:down'" @click="tailscaleDown">停用</el-button>
          <el-button size="small" @click="load">刷新状态</el-button>
        </div>
      </div>
    </div>

    <!-- 编辑 frpc.ini 弹窗 -->
    <el-dialog
      v-model="editVisible"
      :title="`编辑 frpc.ini · ${editInst?.name || ''}`"
      width="780px"
      :close-on-click-modal="false"
    >
      <el-alert type="info" :closable="false" show-icon style="margin-bottom: 10px"
        title="说明"
        description="完整编辑 frpc.ini 内容：[common] 段（server_addr/server_port 等）+ 各 [tunnel] 段。支持直接上传 frpc.ini 文件或粘贴文本。"
      />
      <el-upload
        :show-file-list="false" :auto-upload="false" :multiple="false"
        accept=".ini,.toml,.conf,text/*"
        :on-change="(f: any) => f?.raw && handleUpload(f.raw as File)"
        style="margin-bottom: 8px"
      >
        <el-button size="small" :icon="Upload">上传 frpc.ini 文件</el-button>
      </el-upload>
      <el-input
        v-model="editContent"
        type="textarea"
        :rows="22"
        :spellcheck="false"
        style="font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 12.5px"
        placeholder="[common]&#10;server_addr = your.frps.host&#10;server_port = 7000&#10;&#10;[web]&#10;type = tcp&#10;local_ip = 127.0.0.1&#10;local_port = 8080&#10;remote_port = 18080"
      />
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" :loading="editSaving" @click="saveFrpcIni">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>