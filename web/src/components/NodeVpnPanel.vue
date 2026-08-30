<script setup lang="ts">
// VPN 跳板（单节点维度）：本节点作为独立 VPN 出口的配置面板。
// - 检查本节点 xray 部署状态（ecp-xray-<name> systemd 单元）
// - 填本节点的 UUID / 域名 / WS 路径（与 deploy/xray-vpn.sh 输出对应）
// - 保存到 localStorage（按 node_id 记），导出时汇总全部节点的跳板
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'

import { api, ResultStatus, type ApiNode, type CommandResult } from '@/api/client'

const props = defineProps<{ node: ApiNode }>()

// —— 本节点 xray 部署状态 ——
const xrayStatus = ref<'checking' | 'running' | 'stopped' | 'absent' | 'error'>('checking')
const xrayDetail = ref('')
const deploying = ref(false)

async function checkXray() {
  xrayStatus.value = 'checking'
  try {
    // 用 SHELL 类型查 ecp-xray-* 单元状态（agent 无专用的 systemd_status 命令）
    const r = await api.execCommand(props.node.id, 'shell', { command: "systemctl list-units --all 'ecp-xray-*' --no-pager --plain --no-legend; echo '---'; systemctl is-active 'ecp-xray-*' 2>&1 || true" }, 20)
    const text = (r.stdout || '') + (r.message || '')
    if (/(\S+)\.service\s+loaded\s+active\s+running/.test(text)) {
      xrayStatus.value = 'running'
      xrayDetail.value = text.split('\n').slice(0, 4).join('\n')
    } else if (/No units found|could not be found|不存在|no units|0 loaded units/.test(text)) {
      xrayStatus.value = 'absent'
      xrayDetail.value = '本节点尚未部署 xray（VPN 跳板），点下方「一键部署」。'
    } else if (/inactive|failed|unknown/.test(text)) {
      xrayStatus.value = 'stopped'
      xrayDetail.value = text.trim() || 'xray 单元存在但未运行'
    } else {
      xrayStatus.value = 'absent'
      xrayDetail.value = text.trim() || '未发现 xray 单元'
    }
  } catch (e: any) {
    xrayStatus.value = 'error'
    xrayDetail.value = e?.message || '查询失败'
  }
}

// —— 一键部署跳板（agent 免密 sudo 自动执行；否则提示手动执行）——
async function deployJump() {
  if (deploying.value) return
  deploying.value = true
  try {
    const r = await api.execCommand(props.node.id, 'net_set', { action: 'vpn_deploy' }, 300)
    await handleDeployResult(r)
    await checkXray()
  } catch (e: any) {
    ElMessage.error(`部署失败：${e?.message || ''}`)
  } finally {
    deploying.value = false
  }
}

async function handleDeployResult(r: CommandResult | undefined) {
  if (!r) return
  if (r.status === ResultStatus.NEEDS_PRIVILEGE) {
    // agent 无免密 sudo：给出脚本，用户手动执行
    await ElMessageBox.confirm(
      `<div style="font-size:13px;line-height:1.7">
         <p style="margin:0 0 8px">该节点未配置免密 sudo，请在节点 <b>${props.node.hostname || props.node.id}</b> 上以 root 执行：</p>
         <pre style="background:var(--el-fill-color-light);padding:10px;border-radius:6px;overflow:auto;font-size:12px;white-space:pre-wrap">${(r.privilege_script || '').replace(/</g, '&lt;')}</pre>
         <p style="color:var(--el-text-color-secondary);margin-top:8px">执行完点「已执行，重新检测」。</p>
       </div>`,
      '部署跳板需要提权',
      { dangerouslyUseHTMLString: true, confirmButtonText: '已执行，重新检测', cancelButtonText: '关闭', showClose: false },
    ).then(async () => {
      await checkXray()
    }).catch(() => null)
    return
  }
  const out = (r.stdout || '') + (r.message || '')
  if (/DEPLOY_OK/.test(out)) {
    // 自动回填 UUID
    const m = out.match(/uuid=([0-9a-f-]{36})/i)
    if (m?.[1] && !jump.value.uuid) jump.value.uuid = m[1].toLowerCase()
    if (!jump.value.name) jump.value.name = props.node.hostname || props.node.id
    ElMessage.success('跳板部署成功，已自动填入 UUID')
    xrayDetail.value = out
  } else if (/DOWNLOAD_FAIL/.test(out)) {
    ElMessage.error('部署失败：节点无法访问 GitHub 下载 xray，需手动下载二进制')
    xrayDetail.value = out
  } else {
    ElMessage.warning('部署结果：' + out.slice(0, 300))
    xrayDetail.value = out
  }
}

// —— 本节点跳板配置（localStorage 按 node_id 存取）——
const STORE_KEY = 'ecp.vpn.jumps'
interface JumpConfig { name: string; server: string; port: number; uuid: string; path: string }
const jump = ref<JumpConfig>({ name: '', server: '', port: 443, uuid: '', path: '/ecp-vpn' })
const saved = ref(false)

function loadJump() {
  try {
    const all = JSON.parse(localStorage.getItem(STORE_KEY) || '{}')
    if (all[props.node.id]) {
      jump.value = { ...jump.value, ...all[props.node.id] }
      saved.value = true
    }
    if (!jump.value.name) jump.value.name = props.node.hostname || props.node.id
  } catch { /* ignore */ }
}

function saveJump() {
  if (!jump.value.server || !jump.value.uuid) {
    ElMessage.warning('请先填写本节点入口域名与 UUID（来自 xray-vpn.sh 输出）')
    return
  }
  try {
    const all = JSON.parse(localStorage.getItem(STORE_KEY) || '{}')
    all[props.node.id] = { ...jump.value }
    localStorage.setItem(STORE_KEY, JSON.stringify(all))
    saved.value = true
    ElMessage.success('已保存本节点跳板配置')
  } catch (e: any) {
    ElMessage.error(`保存失败：${e?.message || ''}`)
  }
}

function clearJump() {
  try {
    const all = JSON.parse(localStorage.getItem(STORE_KEY) || '{}')
    delete all[props.node.id]
    localStorage.setItem(STORE_KEY, JSON.stringify(all))
    jump.value = { name: props.node.hostname || props.node.id, server: '', port: 443, uuid: '', path: '/ecp-vpn' }
    saved.value = false
    ElMessage.success('已清除本节点跳板配置')
  } catch { /* ignore */ }
}

// —— 生成 UUID ——
function genUuid() {
  const s: string[] = []
  for (let i = 0; i < 36; i++) {
    const r = Math.floor(Math.random() * 16).toString(16)
    s.push([8, 13, 18, 23].includes(i) ? '-' : r)
  }
  jump.value.uuid = s.join('')
}

const statusLabel = computed(() => ({
  checking: '检测中…',
  running: '已运行',
  stopped: '已停止',
  absent: '未部署',
  error: '查询异常',
} as Record<string, string>)[xrayStatus.value])

const statusType = computed(() => ({
  checking: 'info', running: 'success', stopped: 'warning', absent: 'info', error: 'danger',
} as Record<string, any>)[xrayStatus.value])

onMounted(() => { checkXray(); loadJump() })
</script>

<template>
  <div style="display: flex; flex-direction: column; gap: 14px">
    <!-- 部署状态 -->
    <div class="card">
      <div class="card-header">
        <span>本节点 VPN 状态</span>
        <el-tag size="small" :type="statusType">{{ statusLabel }}</el-tag>
      </div>
      <div class="card-body">
        <pre v-if="xrayDetail" class="mono" style="white-space:pre-wrap;font-size:12px;margin:0 0 8px">{{ xrayDetail }}</pre>
        <div style="display: flex; gap: 8px; flex-wrap: wrap">
          <el-button size="small" type="primary" :loading="deploying" @click="deployJump">
            {{ xrayStatus === 'running' ? '重新部署' : '一键部署跳板' }}
          </el-button>
          <el-button size="small" @click="checkXray">重新检测</el-button>
        </div>
        <el-alert v-if="xrayStatus === 'running'" type="success" :closable="false" show-icon style="margin-top: 10px"
          title="部署成功！下一步：配 Cloudflare Tunnel"
          :description="`Zero Trust → Tunnels → 给本节点建隧道：${props.node.hostname || 'node1'}.vpn.你的域名 → http://127.0.0.1:8444。然后把域名填到下方表单保存。`" />
      </div>
    </div>

    <!-- 本节点跳板配置 -->
    <div class="card">
      <div class="card-header">
        <span>本节点跳板配置</span>
        <el-tag v-if="saved" size="small" type="success">已保存</el-tag>
      </div>
      <div class="card-body">
        <el-form label-width="96px" size="small" label-position="left">
          <el-form-item label="节点名">
            <el-input v-model="jump.name" placeholder="Clash 中显示的节点名" />
          </el-form-item>
          <el-form-item label="入口域名">
            <el-input v-model="jump.server" :placeholder="`${props.node.hostname || 'node1'}.vpn.你的域名`" />
          </el-form-item>
          <el-form-item label="端口">
            <el-input-number v-model="jump.port" :min="1" :max="65535" style="width: 140px" />
          </el-form-item>
          <el-form-item label="UUID">
            <div style="display: flex; gap: 6px; width: 100%">
              <el-input v-model="jump.uuid" placeholder="xray-vpn.sh 输出的 UUID" />
              <el-button size="small" @click="genUuid">生成</el-button>
            </div>
          </el-form-item>
          <el-form-item label="WS 路径">
            <el-input v-model="jump.path" placeholder="/ecp-vpn" />
          </el-form-item>
          <el-form-item>
            <div style="display: flex; gap: 8px">
              <el-button type="primary" @click="saveJump">保存本节点配置</el-button>
              <el-button v-if="saved" @click="clearJump">清除</el-button>
            </div>
          </el-form-item>
        </el-form>
      </div>
    </div>
  </div>
</template>