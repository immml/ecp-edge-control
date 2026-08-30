<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Refresh, Edit, Plus, View, VideoPlay, VideoPause } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'

import { api, type ApiNode, type CommandResult } from '@/api/client'

const nodes = ref<ApiNode[]>([])
const loading = ref(false)
const busy = ref<string>('')

interface FrpInstance {
  name: string
  source: string // systemd | process | config
  unit: string
  running: boolean
  enabled: string
  bin: string
  cmdline: string
  config: string
  configured: boolean
  tunnels?: string[]
}

interface NetState {
  tailscale: { loaded: boolean; ok: boolean; summary: string }
  frp: { loaded: boolean; instances: FrpInstance[] }
}
const states = ref<Record<string, NetState>>({})

function stateOf(nodeId: string): NetState {
  if (!states.value[nodeId]) {
    states.value[nodeId] = {
      tailscale: { loaded: false, ok: false, summary: '—' },
      frp: { loaded: false, instances: [] },
    }
  }
  return states.value[nodeId]
}

async function load() {
  loading.value = true
  try {
    const rows = await api.listNodes()
    nodes.value = rows.filter((n) => n.status === 'online')
    await Promise.all(nodes.value.map((n) => loadNodeState(n)))
  } catch (e: any) {
    ElMessage.error(`加载失败：${e?.message || ''}`)
  } finally {
    loading.value = false
  }
}

async function loadNodeState(n: ApiNode) {
  const s = stateOf(n.id)
  try {
    const r = await api.execCommand(n.id, 'tailscale_status', {})
    s.tailscale.loaded = true
    s.tailscale.ok = String(r.status).includes('OK')
    s.tailscale.summary = summarize(r.stdout, 6)
  } catch {
    s.tailscale.loaded = true
    s.tailscale.summary = '查询失败（可能未安装）'
  }
  try {
    const r = await api.execCommand(n.id, 'frp_status', {})
    s.frp.loaded = true
    try {
      const arr = JSON.parse(r.stdout || '[]')
      s.frp.instances = Array.isArray(arr) ? arr : []
    } catch {
      s.frp.instances = []
    }
  } catch {
    s.frp.loaded = true
  }
}

function summarize(stdout: string, lines: number): string {
  const t = (stdout || '').trim()
  if (!t) return '(空输出)'
  return t.split('\n').slice(0, lines).join('\n')
}

// —— Tailscale 操作 ——
async function doTailscale(n: ApiNode, action: 'up' | 'down') {
  const key = `${n.id}:ts:${action}`
  if (busy.value) return
  busy.value = key
  try {
    const r = await api.execCommand(n.id, `tailscale_${action}`, {})
    await handlePrivilegeResult(n, r, `Tailscale ${action === 'up' ? '启用' : '停用'}`)
    await loadNodeState(n)
  } catch (e: any) {
    ElMessage.error(`操作失败：${e?.message || ''}`)
  } finally {
    busy.value = ''
  }
}

// —— FRP 操作 ——
async function frpInstanceAction(n: ApiNode, inst: FrpInstance, action: 'up' | 'down') {
  const key = `${n.id}:frp:${inst.name}:${action}`
  if (busy.value) return
  busy.value = key
  try {
    const r = await api.execCommand(n.id, `frp_${action}`, { instance: inst.name })
    await handlePrivilegeResult(n, r, `frpc[${inst.name}] ${action === 'up' ? '启动' : '停止'}`)
    await loadNodeState(n)
  } catch (e: any) {
    ElMessage.error(`操作失败：${e?.message || ''}`)
  } finally {
    busy.value = ''
  }
}

// 查看配置
async function viewConfig(n: ApiNode, inst: FrpInstance) {
  try {
    const r = await api.execCommand(n.id, 'frp_config_get', { instance: inst.name })
    const content = r.stdout || '(空配置)'
    await ElMessageBox.alert(
      `<pre style="background:var(--el-fill-color-light);padding:10px;border-radius:6px;overflow:auto;font-size:12px;white-space:pre-wrap;max-height:50vh;margin:0">${content.replace(/</g, '&lt;')}</pre>`,
      `${n.hostname || n.id} · frpc[${inst.name}] 配置（${r.message || '—'}）`,
      { dangerouslyUseHTMLString: true, confirmButtonText: '关闭' },
    ).catch(() => null)
  } catch (e: any) {
    ElMessage.error(`读取配置失败：${e?.message || ''}`)
  }
}

// 新增隧道
async function addTunnel(n: ApiNode, inst: FrpInstance) {
  try {
    const res = await ElMessageBox.prompt(
      `在 frpc[${inst.name}] 上新增隧道。格式（一行，逗号分隔）：名称,类型,本地端口,远程端口,域名(可选)\n示例：web,tcp,8080,18080,myweb.example.com`,
      '新增隧道',
      {
        inputPlaceholder: 'web,tcp,8080,18080,myweb.example.com',
        inputValidator: (v: string) => (v && v.split(',').length >= 3 ? true : '格式：名称,类型,本地端口,远程端口[,域名]'),
      },
    ).catch(() => null)
    if (!res || !res.value) return
    const parts = (res.value as string).split(',').map((s: string) => s.trim())
    const [name, type, localPort, remotePort, domain] = parts
    const tunnel: Record<string, unknown> = {
      name,
      type: type || 'tcp',
      local_port: Number(localPort),
    }
    if (remotePort && Number(remotePort) > 0) tunnel.remote_port = Number(remotePort)
    if (domain) tunnel.custom_domains = domain
    const r = await api.execCommand(n.id, 'frp_config_set', {
      instance: inst.name,
      tunnel: JSON.stringify(tunnel),
    })
    await handlePrivilegeResult(n, r, `新增隧道 ${name}`)
    await loadNodeState(n)
  } catch (e: any) {
    ElMessage.error(`新增隧道失败：${e?.message || ''}`)
  }
}

// 编辑配置（全量）
async function editConfig(n: ApiNode, inst: FrpInstance) {
  try {
    const r = await api.execCommand(n.id, 'frp_config_get', { instance: inst.name })
    const cur = r.stdout || ''
    const res = await ElMessageBox.prompt(
      '编辑 frpc 配置（ini 格式）。保存后需手动重启 frpc 生效。',
      `编辑配置 · ${n.hostname || n.id} · frpc[${inst.name}]`,
      {
        inputType: 'textarea',
        inputValue: cur,
        inputPlaceholder: '[common]\nserver_addr = 你的frps地址\nserver_port = 7000\n\ntype = tcp\n...',
        inputValidator: (v: string) => (v.trim() ? true : '配置不能为空'),
      },
    ).catch(() => null)
    if (!res || res.value === null || res.value === undefined) return
    const value = res.value as string
    const wr = await api.execCommand(n.id, 'frp_config_set', { instance: inst.name, content: value })
    await handlePrivilegeResult(n, wr, '保存配置')
    await loadNodeState(n)
  } catch (e: any) {
    ElMessage.error(`编辑配置失败：${e?.message || ''}`)
  }
}

async function handlePrivilegeResult(n: ApiNode, r: CommandResult, label: string) {
  if (String(r.status).includes('NEEDS_PRIVILEGE')) {
    const script = r.privilege_script || ''
    await ElMessageBox.confirm(
      `<div style="font-size:13px;line-height:1.7">
         <p style="margin:0 0 8px">${r.privilege_hint || '该操作需要 root 权限'}（平台不自行提权）。</p>
         <p style="margin:0 0 6px">请在节点 <b>${n.hostname || n.id}</b> 上执行：</p>
         <pre style="background:var(--el-fill-color-light);padding:10px;border-radius:6px;overflow:auto;font-size:12px;white-space:pre-wrap">${script.replace(/</g, '&lt;')}</pre>
       </div>`,
      `${n.hostname || n.id} · ${label}需要提权`,
      { dangerouslyUseHTMLString: true, confirmButtonText: '我已执行', cancelButtonText: '取消', showClose: false },
    ).catch(() => null)
  } else if (String(r.status).includes('OK')) {
    ElMessage.success(`${n.hostname || n.id} · ${label}成功`)
  } else {
    ElMessage.warning(`${n.hostname || n.id} · ${label}：${r.message || '未成功'}`)
  }
}

onMounted(load)
</script>

<template>
  <div style="display: flex; flex-direction: column; gap: 14px">
    <div style="display: flex; align-items: center; justify-content: space-between">
      <div class="text-secondary" style="font-size: 13px">
        双路组网：<b>Tailscale</b>（主通道，WireGuard mesh）＋ <b>FRP</b>（备用中继，支持多 frpc 实例：
        ChmlFrp / HayFRP / 自建 frps 等）。共 {{ nodes.length }} 个在线节点。
      </div>
      <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
    </div>

    <el-empty v-if="!loading && nodes.length === 0" description="当前没有在线节点" />

    <el-card v-for="n in nodes" :key="n.id" shadow="never">
      <template #header>
        <div style="display: flex; align-items: center; gap: 8px">
          <span class="status-dot is-online" />
          <b>{{ n.hostname || n.id }}</b>
          <span class="mono text-secondary" style="font-size: 12px">{{ n.id }}</span>
          <span v-if="n.tailscale_ip" class="mono text-secondary" style="font-size: 12px">{{ n.tailscale_ip }}</span>
          <span style="flex: 1"></span>
        </div>
      </template>

      <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 16px">
        <!-- Tailscale 卡片 -->
        <div class="card" style="margin: 0">
          <div class="card-header">
            <span>Tailscale</span>
            <el-tag size="small" :type="stateOf(n.id).tailscale.ok ? 'success' : 'info'">
              {{ stateOf(n.id).tailscale.ok ? '已连接' : '—' }}
            </el-tag>
          </div>
          <div class="card-body">
            <pre v-loading="!stateOf(n.id).tailscale.loaded" style="white-space: pre-wrap; font-size: 12px; line-height: 1.6; margin: 0; min-height: 40px">{{ stateOf(n.id).tailscale.summary }}</pre>
            <div style="display: flex; gap: 8px; margin-top: 10px">
              <el-button size="small" type="primary" :loading="busy === `${n.id}:ts:up`" @click="doTailscale(n, 'up')">启用</el-button>
              <el-button size="small" type="warning" :loading="busy === `${n.id}:ts:down`" @click="doTailscale(n, 'down')">停用</el-button>
            </div>
          </div>
        </div>

        <!-- FRP 多实例卡片 -->
        <div class="card" style="margin: 0">
          <div class="card-header">
            <span>FRP（多 frpc 实例）</span>
            <el-tag size="small" :type="stateOf(n.id).frp.instances.some((i) => i.running) ? 'success' : 'info'">
              {{ stateOf(n.id).frp.instances.some((i) => i.running) ? '有实例运行' : '未运行' }}
            </el-tag>
          </div>
          <div class="card-body" v-loading="!stateOf(n.id).frp.loaded">
            <el-empty v-if="stateOf(n.id).frp.loaded && stateOf(n.id).frp.instances.length === 0" description="未发现 frpc 实例" :image-size="40" />
            <div v-for="inst in stateOf(n.id).frp.instances" :key="inst.name" style="border: 1px solid var(--el-border-color-lighter); border-radius: 6px; padding: 8px 10px; margin-bottom: 8px">
              <div style="display: flex; align-items: center; gap: 6px; flex-wrap: wrap">
                <b style="font-size: 13px">{{ inst.name }}</b>
                <el-tag size="small" :type="inst.running ? 'success' : 'info'">{{ inst.running ? '运行中' : '未运行' }}</el-tag>
                <el-tag v-if="inst.enabled === 'enabled'" size="small" type="primary">自启</el-tag>
                <el-tag v-if="inst.configured" size="small">配置:{{ inst.config }}</el-tag>
                <span v-if="inst.tunnels?.length" class="text-secondary" style="font-size: 12px">隧道: {{ inst.tunnels.join(', ') }}</span>
              </div>
              <div v-if="inst.cmdline" class="mono text-secondary" style="font-size: 11.5px; margin-top: 4px; word-break: break-all">{{ inst.cmdline }}</div>
              <div style="display: flex; gap: 6px; margin-top: 6px; flex-wrap: wrap">
                <el-button size="small" :icon="VideoPlay" :loading="busy === `${n.id}:frp:${inst.name}:up`" @click="frpInstanceAction(n, inst, 'up')">启动</el-button>
                <el-button size="small" :icon="VideoPause" :loading="busy === `${n.id}:frp:${inst.name}:down`" @click="frpInstanceAction(n, inst, 'down')">停止</el-button>
                <el-button size="small" :icon="View" @click="viewConfig(n, inst)">配置</el-button>
                <el-button size="small" :icon="Edit" @click="editConfig(n, inst)">编辑</el-button>
                <el-button size="small" type="primary" plain :icon="Plus" @click="addTunnel(n, inst)">新增隧道</el-button>
              </div>
            </div>
            <div class="text-secondary" style="font-size: 12px; margin-top: 4px">
              ※ 编辑配置/新增隧道/启停若需 root 会弹出可复制脚本；免密 sudo 的节点直接执行。
            </div>
          </div>
        </div>
      </div>
    </el-card>
  </div>
</template>
