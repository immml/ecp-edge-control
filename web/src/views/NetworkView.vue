<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Refresh, VideoPlay, VideoPause, Upload, Download } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'

import { api, ResultStatus, type ApiNode, type CommandResult } from '@/api/client'
import NetworkPanel from '@/components/NetworkPanel.vue'

const activeTab = ref('mesh')
const nodes = ref<ApiNode[]>([])

// —— VPN / Clash ——
const vpn = ref({ name: 'ecp-vpn', server: '', port: 443, uuid: '', path: '/ecp-vpn', extra_ips: '' })
const vpnBusy = ref(false)
async function exportClash() {
  if (!vpn.value.server || !vpn.value.uuid) {
    ElMessage.warning('请填写公网入口域名（vpn.xxx）与 UUID')
    return
  }
  vpnBusy.value = true
  try {
    const yaml = await api.exportClash({ ...vpn.value })
    const blob = new Blob([yaml], { type: 'application/x-yaml' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `clash-${vpn.value.server}.yaml`
    a.click()
    URL.revokeObjectURL(a.href)
    ElMessage.success('Clash 配置已下载（导入 Clash 客户端后内网段走 VPN）')
  } catch (e: any) {
    ElMessage.error(`导出失败：${e?.message || ''}`)
  } finally {
    vpnBusy.value = false
  }
}
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

interface TailscaleState {
  loaded: boolean
  loggedIn: boolean
  summary: string
}
interface NetState {
  ts: TailscaleState
  frp: { loaded: boolean; instances: FrpInstance[] }
}
const states = ref<Record<string, NetState>>({})

function stateOf(nodeId: string): NetState {
  if (!states.value[nodeId]) {
    states.value[nodeId] = {
      ts: { loaded: false, loggedIn: false, summary: '—' },
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
  // Tailscale
  try {
    const r = await api.execCommand(n.id, 'tailscale_status', {})
    const text = (r.stdout || '').trim()
    s.ts.loaded = true
    s.ts.loggedIn = !!text && !text.startsWith('Log')
    s.ts.summary = summarizeTs(text)
  } catch {
    s.ts.loaded = true
    s.ts.summary = '查询失败'
    s.ts.loggedIn = false
  }
  // FRP
  try {
    const r = await api.execCommand(n.id, 'frp_status', {})
    try {
      s.frp.instances = JSON.parse(r.stdout || '[]') || []
    } catch {
      s.frp.instances = []
    }
    s.frp.loaded = true
  } catch {
    s.frp.loaded = true
  }
}

function summarizeTs(stdout: string): string {
  const t = (stdout || '').trim()
  if (!t || t.startsWith('Log')) return '未登录'
  const lines = t.split('\n').slice(0, 4)
  return lines.join('\n')
}

// —— Tailscale 登录 ——
async function tailscaleLogin(n: ApiNode) {
  const key = `${n.id}:ts:login`
  if (busy.value) return
  busy.value = key
  try {
    const r = await api.execCommand(n.id, 'tailscale_login_url', {})
    const j = JSON.parse(r.stdout || '{}')
    if (j.logged_in && !j.url) {
      ElMessage.success(`${n.hostname || n.id} 已登录`)
      await loadNodeState(n)
      return
    }
    if (j.url) {
      // 在新标签页打开登录 URL + 显示提示
      window.open(j.url, '_blank', 'noopener')
      await ElMessageBox.confirm(
        `<div style="font-size:13px;line-height:1.7">
           <p>已在新标签页打开 Tailscale 登录链接，请在 <b>login.tailscale.com</b> 完成授权后回到这里。</p>
           <p>登录链接：<a href="${j.url}" target="_blank" class="mono" style="word-break:break-all">${j.url}</a></p>
           <p style="color:var(--el-text-color-secondary);margin-top:8px">完成授权后点「已完成」刷新状态。</p>
         </div>`,
        `${n.hostname || n.id} · Tailscale 登录`,
        {
          dangerouslyUseHTMLString: true,
          confirmButtonText: '已完成，刷新状态',
          cancelButtonText: '稍后',
        },
      ).catch(() => null)
      await loadNodeState(n)
    } else {
      ElMessage.warning(`未获取到登录链接：${j.raw || r.message || ''}`)
    }
  } catch (e: any) {
    ElMessage.error(`登录失败：${e?.message || ''}`)
  } finally {
    busy.value = ''
  }
}

// —— Tailscale 停用/登出 ——
async function tailscaleDown(n: ApiNode) {
  await doTailscaleAction(n, 'tailscale_down', '停用')
}
async function doTailscaleAction(n: ApiNode, type: string, label: string) {
  const key = `${n.id}:ts:${type}`
  if (busy.value) return
  busy.value = key
  try {
    const r = await api.execCommand(n.id, type, {})
    await handlePrivilegeResult(n, r, label)
    await loadNodeState(n)
  } catch (e: any) {
    ElMessage.error(`${label}失败：${e?.message || ''}`)
  } finally {
    busy.value = ''
  }
}

// —— FRP 启停 ——
async function frpUp(n: ApiNode, inst: FrpInstance) {
  await frpAction(n, inst, 'up')
}
async function frpDown(n: ApiNode, inst: FrpInstance) {
  await frpAction(n, inst, 'down')
}
async function frpAction(n: ApiNode, inst: FrpInstance, action: 'up' | 'down') {
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

// —— FRP 编辑 frpc.ini ——
const editVisible = ref(false)
const editInst = ref<FrpInstance | null>(null)
const editContent = ref('')
const editSaving = ref(false)

async function editFrpcIni(n: ApiNode, inst: FrpInstance) {
  try {
    const r = await api.execCommand(n.id, 'frp_config_get', { instance: inst.name })
    editInst.value = inst
    editContent.value = r.stdout || ''
    editVisible.value = true
  } catch (e: any) {
    ElMessage.error(`读取配置失败：${e?.message || ''}`)
  }
}

async function saveFrpcIni(n: ApiNode) {
  if (!editInst.value) return
  editSaving.value = true
  try {
    const r = await api.execCommand(n.id, 'frp_config_set', {
      instance: editInst.value.name,
      content: editContent.value,
    })
    if (r.status === ResultStatus.NEEDS_PRIVILEGE) {
      await showPrivilegeDialog(n, '保存 frpc.ini', r)
    } else if (r.status === ResultStatus.OK) {
      ElMessage.success('保存成功')
      editVisible.value = false
    } else {
      ElMessage.warning(`保存：${r.message || '未成功'}`)
    }
    await loadNodeState(n)
  } catch (e: any) {
    ElMessage.error(`保存失败：${e?.message || ''}`)
  } finally {
    editSaving.value = false
  }
}

// 上传 frpc.ini 到 textarea
function handleUpload(file: File) {
  const reader = new FileReader()
  reader.onload = () => { editContent.value = String(reader.result || '') }
  reader.readAsText(file)
  return false // 阻止 el-upload 默认行为
}

async function handlePrivilegeResult(n: ApiNode, r: CommandResult, label: string) {
  if (r.status === ResultStatus.NEEDS_PRIVILEGE) {
    await showPrivilegeDialog(n, label, r)
  } else if (r.status === ResultStatus.OK) {
    ElMessage.success(`${n.hostname || n.id} · ${label}成功`)
  } else {
    ElMessage.warning(`${n.hostname || n.id} · ${label}：${r.message || '未成功'}`)
  }
}

async function showPrivilegeDialog(n: ApiNode, label: string, r: CommandResult) {
  const script = r.privilege_script || ''
  await ElMessageBox.confirm(
    `<div style="font-size:13px;line-height:1.7">
       <p style="margin:0 0 8px">${r.privilege_hint || '该操作需要 root 权限'}（平台不自行提权）。</p>
       <p style="margin:0 0 6px">请在节点 <b>${n.hostname || n.id}</b> 上执行：</p>
       <pre style="background:var(--el-fill-color-light);padding:10px;border-radius:6px;overflow:auto;font-size:12px;white-space:pre-wrap">${script.replace(/</g, '&lt;')}</pre>
     </div>`,
    `${n.hostname || n.id} · ${label}需要提权`,
    { dangerouslyUseHTMLString: true, confirmButtonText: '我已执行', cancelButtonText: '关闭', showClose: false },
  ).catch(() => null)
}

onMounted(load)
</script>

<template>
  <el-tabs v-model="activeTab">
    <el-tab-pane label="组网（Tailscale / FRP）" name="mesh">
      <div style="display: flex; flex-direction: column; gap: 14px">
    <div style="display: flex; align-items: center; justify-content: space-between">
      <div class="text-secondary" style="font-size: 13px">
        组网管理 · {{ nodes.length }} 个在线节点。FRP 多 frpc 实例（ChmlFrp / HayFRP / 自建 frps）+ Tailscale mesh。
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
        <!-- FRP 左：编辑 frpc.ini + 实例操作 -->
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
                <el-tag v-if="inst.configured" size="small">ini: {{ inst.config }}</el-tag>
              </div>
              <div v-if="inst.cmdline" class="mono text-secondary" style="font-size: 11.5px; margin-top: 4px; word-break: break-all">{{ inst.cmdline }}</div>
              <div style="display: flex; gap: 6px; margin-top: 6px; flex-wrap: wrap">
                <el-button size="small" :icon="VideoPlay" :loading="busy === `${n.id}:frp:${inst.name}:up`" @click="frpUp(n, inst)">启动</el-button>
                <el-button size="small" :icon="VideoPause" :loading="busy === `${n.id}:frp:${inst.name}:down`" @click="frpDown(n, inst)">停止</el-button>
                <el-button size="small" type="primary" plain @click="editFrpcIni(n, inst)">编辑 frpc.ini</el-button>
              </div>
              <div v-if="inst.tunnels?.length" class="text-secondary" style="font-size: 11.5px; margin-top: 4px">
                隧道段: {{ inst.tunnels.join(', ') }}
              </div>
            </div>
          </div>
        </div>

        <!-- Tailscale 右：登录态 + 登录入口 -->
        <div class="card" style="margin: 0">
          <div class="card-header">
            <span>Tailscale（组网 / 登录）</span>
            <el-tag v-if="stateOf(n.id).ts.loaded" size="small" :type="stateOf(n.id).ts.loggedIn ? 'success' : 'warning'">
              {{ stateOf(n.id).ts.loggedIn ? '已登录' : '未登录' }}
            </el-tag>
          </div>
          <div class="card-body" v-loading="!stateOf(n.id).ts.loaded">
            <pre v-if="stateOf(n.id).ts.loggedIn" style="white-space: pre-wrap; font-size: 12px; line-height: 1.6; margin: 0; min-height: 60px">{{ stateOf(n.id).ts.summary }}</pre>
            <div v-else style="min-height: 60px">
              <el-alert type="warning" :closable="false" show-icon title="节点未登录 tailnet" description="点击下方「登录」按钮，会在新标签页打开 Tailscale 官方认证页面，完成授权后自动激活。" />
            </div>
            <div style="display: flex; gap: 8px; margin-top: 12px; flex-wrap: wrap">
              <el-button v-if="!stateOf(n.id).ts.loggedIn" type="primary" :loading="busy === `${n.id}:ts:login`" @click="tailscaleLogin(n)">登录</el-button>
              <el-button v-if="stateOf(n.id).ts.loggedIn" type="warning" :loading="busy === `${n.id}:ts:down`" @click="tailscaleDown(n)">停用</el-button>
              <el-button size="small" @click="loadNodeState(n)">刷新状态</el-button>
            </div>
          </div>
        </div>
      </div>
    </el-card>

    <!-- 编辑 frpc.ini 弹窗 -->
    <el-dialog
      v-model="editVisible"
      :title="`编辑 frpc.ini · ${editInst?.name || ''}`"
      width="780px"
      :close-on-click-modal="false"
    >
      <el-alert type="info" :closable="false" show-icon style="margin-bottom: 10px"
        title="说明"
        description="完整编辑 frpc.ini 内容：[common] 段（server_addr/server_port 等）+ 各 [tunnel] 段（type/local_port/remote_port 等）。支持直接上传 frpc.ini 文件或粘贴文本。"
      />
      <el-upload
        :show-file-list="false"
        :auto-upload="false"
        :multiple="false"
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
    </el-tab-pane>
    <el-tab-pane label="网络管理（WiFi / 网卡 / 测速 / 虚拟MAC）" name="net">
      <div style="display: flex; flex-direction: column; gap: 14px">
        <div class="text-secondary" style="font-size: 13px">
          网络管理 · 扫描 WiFi、连接、以太网/IP（DHCP / 手动 / PPPoE）、信道、ping、测速、虚拟 MAC。
        </div>
        <NetworkPanel v-for="n in nodes" :key="'net-' + n.id" :node="n" />
      </div>
    </el-tab-pane>
    <el-tab-pane label="VPN / Clash" name="vpn">
      <div class="text-secondary" style="font-size: 13px; margin-bottom: 10px">
        VPN 网关（xray VMess+WS 经 Cloudflare Tunnel 暴露）→ 导出 Clash 配置，在外网即可访问这些内网设备。
        网关部署：节点上执行 <code class="mono">sudo bash deploy/xray-vpn.sh</code>，再按脚本提示建 Cloudflare Tunnel（vpn.你的域名 → http://127.0.0.1:8444）。
      </div>
      <el-card shadow="never" style="max-width: 560px">
        <el-form label-width="110px" size="small">
          <el-form-item label="入口域名">
            <el-input v-model="vpn.server" placeholder="vpn.immml.top（Cloudflare Tunnel Hostname）" />
          </el-form-item>
          <el-form-item label="端口">
            <el-input-number v-model="vpn.port" :min="1" :max="65535" style="width: 140px" />
          </el-form-item>
          <el-form-item label="UUID">
            <el-input v-model="vpn.uuid" placeholder="xray 生成（如 3f7c...-.... ）" />
          </el-form-item>
          <el-form-item label="WS 路径">
            <el-input v-model="vpn.path" placeholder="/ecp-vpn（与 xray-vpn.sh --path 一致）" />
          </el-form-item>
          <el-form-item label="节点名">
            <el-input v-model="vpn.name" placeholder="ecp-vpn" />
          </el-form-item>
          <el-form-item label="附加网段">
            <el-input v-model="vpn.extra_ips" placeholder="可选，逗号分隔，默认含 192.168/16、10/8、100.64/10" />
          </el-form-item>
          <el-form-item>
            <el-button type="primary" :loading="vpnBusy" @click="exportClash">
              <el-icon style="margin-right: 4px"><Download /></el-icon>导出 Clash 配置
            </el-button>
          </el-form-item>
        </el-form>
      </el-card>
    </el-tab-pane>
  </el-tabs>
</template>
