<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'

import { api, type TelemetrySample } from '@/api/client'
import { mockNodes } from '@/mock/nodes'
import { formatBytes, percent, timeAgo } from '@/utils/format'
import NetworkPanel from '@/components/NetworkPanel.vue'
import NodeMeshPanel from '@/components/NodeMeshPanel.vue'
import NodeVpnPanel from '@/components/NodeVpnPanel.vue'

const route = useRoute()
const router = useRouter()
const nodeId = route.params.id as string

const loading = ref(true)
const node = ref<any>(null)
const online = ref(false)
const history = ref<TelemetrySample[]>([]) // 升序（旧→新）
const detailTab = ref('overview') // overview / net / mesh / vpn

// OTA 升级
const upgradeVisible = ref(false)
const upgradeFile = ref<File | null>(null)
const upgrading = ref(false)

function openUpgrade() {
  upgradeFile.value = null
  upgradeVisible.value = true
}

async function doUpgrade() {
  if (!upgradeFile.value) return
  upgrading.value = true
  try {
    const up = await api.uploadAgent(upgradeFile.value)
    const res = await api.upgradeNode(nodeId, up.name)
    upgradeVisible.value = false
    if (res.needs_privilege) {
      const script = res.privilege_script || ''
      await ElMessageBox.confirm(
        `<div style="font-size:13px;line-height:1.7">
           <p style="margin:0 0 8px">Agent 升级需要 root 权限，请在节点执行：</p>
           <pre style="background:var(--el-fill-color-light);padding:10px;border-radius:6px;overflow:auto;font-size:12px;white-space:pre-wrap">${script.replace(/</g, '&lt;')}</pre>
         </div>`,
        '升级需要提权',
        { dangerouslyUseHTMLString: true, confirmButtonText: '我已执行', cancelButtonText: '关闭', showClose: false },
      ).catch(() => null)
    } else {
      ElMessage.success(`升级已下发（SHA256 ${up.sha256.slice(0, 12)}…），节点将自动重启，稍候刷新页面验证`)
    }
    await new Promise((r) => setTimeout(r, 4000))
    await load()
  } catch (e: any) {
    ElMessage.error(`升级失败：${e?.message || ''}`)
  } finally {
    upgrading.value = false
  }
}

async function load() {
  try {
    const d = await api.getNode(nodeId)
    node.value = { ...(d as any).node, capabilities: (d as any).view?.capabilities }
    online.value = d.online
  } catch (e: any) {
    // 控制面离线时回退到示例数据，保持页面可浏览
    node.value = mockNodes.find((n: any) => n.id === nodeId) ?? null
    online.value = node.value?.status === 'online'
    ElMessage.warning(`节点信息回退示例数据：${e?.message ?? '未知错误'}`)
  }
  try {
    const t = await api.getTelemetry(nodeId, 120)
    history.value = (t.items ?? []).slice().reverse()
  } catch {
    history.value = []
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  // 兼容旧链接 /nodes/:id/network 重定向之后自动切到网络管理 Tab
  const t = route.query.tab
  if (t === 'network' || t === 'net') detailTab.value = 'net'
  if (t === 'mesh') detailTab.value = 'mesh'
  if (t === 'vpn') detailTab.value = 'vpn'
  load()
  timer = window.setInterval(load, 15000)
})
onBeforeUnmount(() => window.clearInterval(timer))
let timer = 0

const latest = computed<TelemetrySample | null>(() =>
  history.value.length ? history.value[history.value.length - 1] : null,
)
const memPct = computed(() => latest.value ? percent(latest.value.mem_used_bytes, latest.value.mem_total_bytes) : 0)
const diskPct = computed(() => latest.value ? percent(latest.value.disk_used_bytes, latest.value.disk_total_bytes) : 0)

// ---- SVG 折线图 ----
function series(key: (t: TelemetrySample) => number): { pts: string; max: number } {
  const data = history.value.map(key).filter((v) => Number.isFinite(v))
  if (data.length < 2) return { pts: '', max: 100 }
  const max = Math.max(...data, 1) * 1.1
  const w = 340
  const h = 64
  const step = w / (data.length - 1)
  const pts = data
    .map((v, i) => `${(i * step).toFixed(1)},${(h - (v / max) * h).toFixed(1)}`)
    .join(' ')
  return { pts, max }
}
const cpuSeries = computed(() => series((t) => t.cpu_percent))
const memSeries = computed(() => series((t) => percent(t.mem_used_bytes, t.mem_total_bytes)))
const diskSeries = computed(() => series((t) => percent(t.disk_used_bytes, t.disk_total_bytes)))
const tempSeries = computed(() => series((t) => t.temperature_celsius))

function timeLabel(i: number): string {
  const t = history.value[i]
  if (!t) return ''
  return new Date(t.ts).toLocaleTimeString('zh-CN', { hour12: false })
}

// 远程打开节点 1Panel：直连 Tailscale IP（跨网可达），自动带安全入口
function openPanel() {
  const ip = node.value?.tailscale_ip
  if (!ip) {
    ElMessage.warning('该节点没有可用的 Tailscale IP，无法远程打开 1Panel')
    return
  }
  const entrance = node.value?.capabilities?.panelEntrance || ''
  const path = entrance ? (entrance.startsWith('/') ? entrance : '/' + entrance) : ''
  window.open(`http://${ip}:31252${path}`, '_blank', 'noopener')
}
</script>

<template>
  <div v-if="node" style="display: flex; flex-direction: column; gap: 16px">
    <div style="display: flex; align-items: center; gap: 10px">
      <el-button size="small" text @click="router.push('/nodes')">← 返回</el-button>
      <span style="font-weight: 600">{{ node.hostname || node.id }}</span>
      <span class="status-tag" :class="online ? 'is-online' : 'is-offline'">
        {{ online ? '在线' : '离线' }}
      </span>
      <span class="text-secondary" style="font-size: 12px">{{ node.id }}</span>
      <span style="flex: 1"></span>
      <el-button size="small" :disabled="!online" @click="router.push(`/nodes/${nodeId}/containers`)">容器</el-button>
      <el-button size="small" :disabled="!online" @click="router.push(`/nodes/${nodeId}/vnc`)">VNC</el-button>
      <el-button size="small" :disabled="!online" @click="router.push(`/nodes/${nodeId}/terminal`)">终端</el-button>
      <el-button size="small" :disabled="!online" @click="router.push(`/nodes/${nodeId}/files`)">文件</el-button>
      <el-button size="small" :disabled="!node.tailscale_ip" @click="openPanel">1Panel</el-button>
      <el-button size="small" :icon="'Upload'" :loading="upgrading" @click="openUpgrade">升级</el-button>
      <el-button size="small" :icon="'Refresh'" :loading="loading" @click="load">刷新</el-button>
    </div>

    <!-- OTA 升级弹窗 -->
    <el-dialog v-model="upgradeVisible" title="升级 Agent（OTA）" width="560px" :close-on-click-modal="false">
      <el-alert type="info" :closable="false" show-icon style="margin-bottom: 12px"
        title="流程" description="① 上传新的 Agent 二进制（arm64/amd64 按节点架构）→ ② 下发升级 → 节点下载校验 SHA256 后原子替换并重启。升级期间节点会短暂断连，几秒后自动恢复。" />
      <el-upload
        :auto-upload="false" :show-file-list="true" :limit="1"
        accept="*/*" drag
        :on-change="(f: any) => { if (f?.raw) upgradeFile = f.raw as File }"
        :on-remove="() => { upgradeFile = null }"
        style="margin-bottom: 12px"
      >
        <div style="font-size: 13px; padding: 8px 0">拖入或点击选择 Agent 二进制文件</div>
      </el-upload>
      <template #footer>
        <el-button @click="upgradeVisible = false">取消</el-button>
        <el-button type="primary" :loading="upgrading" :disabled="!upgradeFile" @click="doUpgrade">上传并升级</el-button>
      </template>
    </el-dialog>

    <el-tabs v-model="detailTab" class="node-detail-tabs">
      <!-- ============ 概览 ============ -->
      <el-tab-pane label="概览" name="overview">
        <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 16px">
          <div class="card">
            <div class="card-header">系统信息</div>
            <div class="card-body">
              <el-descriptions :column="1" size="small" border>
                <el-descriptions-item label="主机名">{{ node.hostname || '-' }}</el-descriptions-item>
                <el-descriptions-item label="节点 ID">
                  <span class="mono">{{ node.id }}</span>
                </el-descriptions-item>
                <el-descriptions-item label="架构">{{ node.arch }}</el-descriptions-item>
                <el-descriptions-item label="系统">{{ node.os }}</el-descriptions-item>
                <el-descriptions-item label="Agent 版本">{{ node.agent_version }}</el-descriptions-item>
                <el-descriptions-item label="最后在线">{{ timeAgo(node.last_seen_at) }}</el-descriptions-item>
              </el-descriptions>
            </div>
          </div>

          <div class="card">
            <div class="card-header">实时资源</div>
            <div class="card-body">
              <template v-if="latest">
                <div class="metric">
                  <div class="metric-label">
                    <span>CPU</span>
                    <span class="metric-value">{{ latest.cpu_percent.toFixed(1) }}%</span>
                  </div>
                  <el-progress :percentage="Math.round(latest.cpu_percent)" :stroke-width="6" :show-text="false" />
                </div>
                <div class="metric">
                  <div class="metric-label">
                    <span>内存</span>
                    <span class="metric-value">
                      {{ formatBytes(latest.mem_used_bytes) }} / {{ formatBytes(latest.mem_total_bytes) }}
                    </span>
                  </div>
                  <el-progress :percentage="Math.round(memPct)" :stroke-width="6" :show-text="false" />
                </div>
                <div class="metric">
                  <div class="metric-label">
                    <span>磁盘</span>
                    <span class="metric-value">
                      {{ formatBytes(latest.disk_used_bytes) }} / {{ formatBytes(latest.disk_total_bytes) }}
                    </span>
                  </div>
                  <el-progress :percentage="Math.round(diskPct)" :stroke-width="6" :show-text="false" />
                </div>
                <div style="display: flex; gap: 20px; margin-top: 14px; font-size: 12.5px">
                  <div>
                    <div class="text-secondary">负载 (1m)</div>
                    <div class="mono" style="font-size: 15px">{{ latest.load1?.toFixed?.(2) ?? latest.load1 }}</div>
                  </div>
                  <div>
                    <div class="text-secondary">温度</div>
                    <div class="mono" style="font-size: 15px">{{ latest.temperature_celsius?.toFixed?.(1) ?? latest.temperature_celsius }}°C</div>
                  </div>
                  <div>
                    <div class="text-secondary">容器</div>
                    <div class="mono" style="font-size: 15px">{{ latest.containers_running ?? 0 }}</div>
                  </div>
                </div>
              </template>
              <el-empty v-else description="暂无遥测数据（节点上线后自动采集）" :image-size="60" />
            </div>
          </div>
        </div>

        <div class="card" style="margin-top: 16px">
          <div class="card-header">
            <span>遥测趋势</span>
            <span class="text-secondary" style="font-weight: 400; font-size: 12px">
              最近 {{ history.length }} 个采集点 · 15s 自动刷新
            </span>
          </div>
          <div class="card-body" style="display: grid; grid-template-columns: 1fr 1fr; gap: 20px">
            <div v-for="(g, gi) in [
              { label: 'CPU %', pts: cpuSeries.pts, max: cpuSeries.max, color: '#6b37c9' },
              { label: '内存 %', pts: memSeries.pts, max: memSeries.max, color: '#409eff' },
              { label: '磁盘 %', pts: diskSeries.pts, max: diskSeries.max, color: '#67c23a' },
              { label: '温度 °C', pts: tempSeries.pts, max: tempSeries.max, color: '#e6a23c' },
            ]" :key="gi">
              <div class="text-secondary" style="font-size: 12.5px; margin-bottom: 4px">
                {{ g.label }} <span class="mono">(max {{ g.max.toFixed(0) }})</span>
              </div>
              <svg v-if="g.pts" viewBox="0 0 340 64" style="width: 100%; height: 64px; display: block">
                <polyline :points="g.pts" fill="none" :stroke="g.color" stroke-width="2" stroke-linejoin="round" />
                <circle
                  v-for="(pt, pi) in g.pts.split(' ')"
                  :key="pi"
                  :cx="pt.split(',')[0]"
                  :cy="pt.split(',')[1]"
                  r="1.6"
                  :fill="g.color"
                />
              </svg>
              <el-empty v-else description="样本不足" :image-size="40" />
              <div class="text-secondary mono" style="font-size: 11px">
                {{ timeLabel(0) }} → {{ timeLabel(history.length - 1) }}
              </div>
            </div>
          </div>
        </div>
      </el-tab-pane>

      <!-- ============ 网络管理（本节点） ============ -->
      <el-tab-pane label="网络管理" name="net">
        <NetworkPanel v-if="node" :node="node" />
      </el-tab-pane>

      <!-- ============ 组网（本节点） ============ -->
      <el-tab-pane label="组网" name="mesh">
        <NodeMeshPanel v-if="node" :node="node" />
      </el-tab-pane>

      <!-- ============ VPN 跳板（本节点） ============ -->
      <el-tab-pane label="VPN 跳板" name="vpn">
        <NodeVpnPanel v-if="node" :node="node" />
      </el-tab-pane>
    </el-tabs>
  </div>

  <el-empty v-else description="节点不存在" />
</template>
