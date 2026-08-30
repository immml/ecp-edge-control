<script setup lang="ts">
// 网络管理面板：WiFi 扫描/连接、设备信息（DHCP/手动/PPPoE）、信道、ping、测速、
// 虚拟 MAC（保持当前子网掩码自动生成 MAC+IP）。
import { ref } from 'vue'
import { Refresh, Search, Link, Lightning, Monitor, Aim } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, ResultStatus, type ApiNode, type CommandResult } from '@/api/client'

const props = defineProps<{ node: ApiNode }>()

const running = ref('')
const statusText = ref('点击「探测网络」获取设备/IP/信道概览')

// —— 通用执行（net_get / net_set）——
async function run(action: string, params: Record<string, unknown>) {
  if (running.value) return
  running.value = `${action}`
  try {
    const r = await api.execCommandAuto(props.node.id, 'net_get', { action, ...params })
    return r
  } finally {
    running.value = ''
  }
}

function showResult(r: CommandResult | undefined) {
  if (!r) return
  if (r.status === ResultStatus.NEEDS_PRIVILEGE) {
    ElMessageBox.alert(
      `<div style="font-size:13px;">
         <p style="color:var(--el-color-warning);margin-bottom:8px">需要 root 权限，请在节点上执行：</p>
         <pre class="mono" style="white-space:pre-wrap;background:var(--el-fill-color-light);padding:10px;border-radius:6px">${r.message || ''}\n${r.privilege_script || ''}</pre>
       </div>`,
      '需要提权',
      { dangerouslyUseHTMLString: true, confirmButtonText: '已执行，再试一次' },
    )
      .then(() => {})
      .catch(() => {})
    return
  }
  if (r.status === ResultStatus.FAILED) {
    ElMessage.error(`操作失败：${r.message || r.stdout || ''}`)
    return
  }
  if (r.stdout) ElMessage.success('完成')
  else ElMessage.success('已执行')
}

// —— 1. 网络状态探测（设备 + IP + WiFi + 信道）——
async function probe() {
  if (running.value) return
  running.value = 'probe'
  statusText.value = '探测中…'
  try {
    const dev = (await api.execCommandAuto(props.node.id, 'net_get', { action: 'devices' })).stdout || ''
    const ip = (await api.execCommandAuto(props.node.id, 'net_get', { action: 'ip' })).stdout || ''
    const wifi = (await api.execCommandAuto(props.node.id, 'net_get', { action: 'wifi_status' })).stdout || ''
    const ch = (await api.execCommandAuto(props.node.id, 'net_get', { action: 'channel' })).stdout || ''
    statusText.value = `【设备】\n${dev}\n【IP / 网卡】\n${ip}\n【WiFi 当前】\n${wifi}\n【信道】\n${ch}`.trim()
  } catch (e: any) {
    ElMessage.error(`探测失败：${e?.message || ''}`)
  } finally {
    running.value = ''
  }
}

// —— 2. WiFi 扫描与连接 ——
interface WifiRow {
  ssid: string
  signal: string
  security: string
  chan: string
}
const wifiList = ref<WifiRow[]>([])
const wifiOut = ref('')

async function scanWifi() {
  const r = await run('wifi_scan', {})
  if (!r) return
  wifiOut.value = r.stdout || r.message || ''
  const rows: WifiRow[] = []
  for (const line of (r.stdout || '').split('\n')) {
    const parts = line.split(':')
    if (parts.length >= 4) rows.push({ ssid: parts[0], signal: parts[1], security: parts[2], chan: parts[3] })
  }
  wifiList.value = rows
}

async function connectWifi(row: WifiRow) {
  if (row.security && row.security !== '--') {
    const { value } = await ElMessageBox.prompt(`输入 WiFi 密码（${row.ssid}）`, '连接 WiFi', {
      inputType: 'password',
      inputPlaceholder: '密码',
      confirmButtonText: '连接',
      inputValidator: (v) => (v ? true : '请输入密码'),
    }).catch(() => ({ value: '' }))
    if (!value) return
    const r = await api.execCommandAuto(props.node.id, 'net_set', { action: 'wifi_connect', ssid: row.ssid, password: value }, 60)
    showResult(r)
  } else {
    const r = await api.execCommandAuto(props.node.id, 'net_set', { action: 'wifi_connect', ssid: row.ssid }, 60)
    showResult(r)
  }
}

// —— 3. IP 配置（DHCP / 手动 / PPPoE）——
const connSel = ref('')
const modeSel = ref('dhcp')
const manualAddr = ref('')
const manualGw = ref('')
const manualDns = ref('')
const pppIface = ref('')
const pppUser = ref('')
const pppPass = ref('')

async function applyIpMode() {
  if (!connSel.value && modeSel.value !== 'pppoe') {
    ElMessage.warning('请填写或选择要修改的连接')
    return
  }
  let r: CommandResult | undefined
  if (modeSel.value === 'pppoe') {
    r = await api.execCommandAuto(props.node.id, 'net_set', {
      action: 'ip_mode', mode: 'pppoe',
      iface: pppIface.value, username: pppUser.value, password: pppPass.value,
    }, 60)
  } else {
    r = await api.execCommandAuto(props.node.id, 'net_set', {
      action: 'ip_mode', conn: connSel.value, mode: modeSel.value,
      address: manualAddr.value, gateway: manualGw.value, dns: manualDns.value,
    }, 60)
  }
  showResult(r)
}

// —— 4. ping ——
const pingHost = ref('')
const pingOut = ref('')
async function doPing() {
  if (!pingHost.value) {
    ElMessage.warning('请输入目标地址')
    return
  }
  pingOut.value = 'pinging…'
  const r = await api.execCommandAuto(props.node.id, 'net_get', { action: 'ping', host: pingHost.value } as Record<string, unknown>, 20)
  pingOut.value = r?.stdout || r?.message || ''
}

// —— 5. 测速（可能需要 60s+）——
const speedOut = ref('')
async function speedtest() {
  speedOut.value = '测速中（约 30-60 秒）…'
  const r = await api.execCommandAuto(props.node.id, 'net_get', { action: 'speedtest' } as Record<string, unknown>, 120)
  speedOut.value = r?.stdout || r?.message || ''
  if (r?.status === ResultStatus.FAILED) ElMessage.error(r.message || '测速失败')
}

// —— 6. 虚拟 MAC ——
const vmacIface = ref('')
const vmacSsid = ref('')
const vmacOut = ref('')
async function genVirtualMac() {
  if (!vmacSsid.value) {
    ElMessage.warning('请输入要应用的 WiFi SSID')
    return
  }
  const r = await api.execCommandAuto(props.node.id, 'net_set', {
    action: 'virtual_mac', iface: vmacIface.value, ssid: vmacSsid.value,
  }, 60)
  if (r) {
    vmacOut.value = r.stdout || r.message || ''
    showResult(r)
  }
}
</script>

<template>
  <div class="net-panel">
    <!-- 1. 状态探测 -->
    <div class="card">
      <div class="card-head">
        <el-icon><Monitor /></el-icon><span>网络状态</span>
        <el-button size="small" type="primary" :loading="running === 'probe'" @click="probe">
          <el-icon style="margin-right:4px"><Refresh /></el-icon>探测网络
        </el-button>
      </div>
      <pre class="mono-out" v-if="statusText">{{ statusText }}</pre>
    </div>

    <!-- 2. WiFi -->
    <div class="card">
      <div class="card-head">
        <el-icon><Aim /></el-icon><span>WiFi 扫描</span>
        <el-button size="small" :loading="running === 'wifi_scan'" @click="scanWifi">
          <el-icon style="margin-right:4px"><Search /></el-icon>扫描
        </el-button>
      </div>
      <el-table :data="wifiList" size="small" max-height="260" v-if="wifiList.length">
        <el-table-column prop="ssid" label="SSID" min-width="140" />
        <el-table-column prop="signal" label="信号" width="60" />
        <el-table-column prop="chan" label="信道" width="60" />
        <el-table-column prop="security" label="安全" width="90" />
        <el-table-column label="操作" width="80">
          <template #default="{ row }">
            <el-button size="small" type="primary" link @click="connectWifi(row)">
              <el-icon style="margin-right:2px"><Link /></el-icon>连接
            </el-button>
          </template>
        </el-table-column>
      </el-table>
      <pre class="mono-out dim" v-if="wifiOut && !wifiList.length">{{ wifiOut }}</pre>
      <div class="empty" v-if="!wifiList.length && !wifiOut">点「扫描」查看附近 WiFi</div>
    </div>

    <!-- 3. IP 配置 -->
    <div class="card">
      <div class="card-head"><el-icon><Lightning /></el-icon><span>IP 配置（DHCP / 手动 / PPPoE）</span></div>
      <el-form label-width="92px" size="small">
        <el-form-item label="连接">
          <el-input v-model="connSel" placeholder="如 Wired connection 1 / 留空取当前活跃连接" />
        </el-form-item>
        <el-form-item label="方式">
          <el-radio-group v-model="modeSel">
            <el-radio-button value="dhcp">DHCP</el-radio-button>
            <el-radio-button value="manual">手动分配</el-radio-button>
            <el-radio-button value="pppoe">PPPoE</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <template v-if="modeSel === 'manual'">
          <el-form-item label="地址">
            <el-input v-model="manualAddr" placeholder="192.168.1.100/24" />
          </el-form-item>
          <el-form-item label="网关">
            <el-input v-model="manualGw" placeholder="192.168.1.1" />
          </el-form-item>
          <el-form-item label="DNS">
            <el-input v-model="manualDns" placeholder="223.5.5.5, 8.8.8.8" />
          </el-form-item>
        </template>
        <template v-if="modeSel === 'pppoe'">
          <el-form-item label="接口">
            <el-input v-model="pppIface" placeholder="eth0 / wan" />
          </el-form-item>
          <el-form-item label="账号">
            <el-input v-model="pppUser" placeholder="宽带账号" />
          </el-form-item>
          <el-form-item label="密码">
            <el-input v-model="pppPass" type="password" placeholder="宽带密码" />
          </el-form-item>
        </template>
        <el-form-item>
          <el-button type="primary" :loading="running === 'ip_mode'" @click="applyIpMode">应用配置</el-button>
        </el-form-item>
      </el-form>
    </div>

    <!-- 4. ping -->
    <div class="card">
      <div class="card-head"><el-icon><Search /></el-icon><span>Ping 测试</span></div>
      <div class="row">
        <el-input v-model="pingHost" placeholder="目标地址 / IP（如 223.5.5.5、192.168.1.1）" @keyup.enter="doPing" />
        <el-button type="primary" :loading="running === 'ping'" @click="doPing">Ping</el-button>
      </div>
      <pre class="mono-out dim" v-if="pingOut">{{ pingOut }}</pre>
    </div>

    <!-- 5. 测速 -->
    <div class="card">
      <div class="card-head"><el-icon><Lightning /></el-icon><span>速度测试（speedtest-go）</span></div>
      <div class="row">
        <el-button type="primary" :loading="running === 'speedtest'" @click="speedtest">开始测速</el-button>
      </div>
      <pre class="mono-out dim" v-if="speedOut">{{ speedOut }}</pre>
    </div>

    <!-- 6. 虚拟 MAC -->
    <div class="card">
      <div class="card-head"><el-icon><Aim /></el-icon><span>虚拟 MAC + IP（保持当前子网掩码）</span></div>
      <el-form label-width="92px" size="small">
        <el-form-item label="接口">
          <el-input v-model="vmacIface" placeholder="留空自动识别（如 wlan0）" />
        </el-form-item>
        <el-form-item label="WiFi SSID">
          <el-input v-model="vmacSsid" placeholder="要连接的 WiFi 名称" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="running === 'virtual_mac'" @click="genVirtualMac">
            生成虚拟 MAC + IP 并连接
          </el-button>
        </el-form-item>
      </el-form>
      <pre class="mono-out dim" v-if="vmacOut">{{ vmacOut }}</pre>
    </div>
  </div>
</template>

<style scoped>
.net-panel {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.card {
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  padding: 14px;
}
.card-head {
  display: flex;
  align-items: center;
  gap: 6px;
  font-weight: 600;
  margin-bottom: 10px;
}
.mono-out {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-all;
  font-size: 12.5px;
  line-height: 1.6;
  max-height: 320px;
  overflow: auto;
}
.dim {
  color: var(--el-text-color-secondary);
}
.empty {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
.row {
  display: flex;
  gap: 8px;
}
.row .el-input {
  flex: 1;
}
</style>