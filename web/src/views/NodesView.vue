<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Cpu, Timer, Refresh, Promotion } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import type { TableInstance } from 'element-plus'

import { api, type ApiNode, type BatchResult } from '@/api/client'
import { mockNodes } from '@/mock/nodes'
import { formatBytes, percent, timeAgo } from '@/utils/format'

const router = useRouter()
const nodes = ref<ApiNode[]>([])
const loading = ref(false)
const usingMock = ref(false)

const onlineCount = computed(() => nodes.value.filter((n) => n.status === 'online').length)

// —— 批量指令 ——
const tableRef = ref<TableInstance>()
const selected = ref<ApiNode[]>([])
const batchDialog = ref(false)
const batchScript = ref('')
const batchRunning = ref(false)
const batchResults = ref<BatchResult[]>([])

const selectedCount = computed(() => selected.value.length)
const canBatch = computed(() => selectedCount.value > 0 && !batchRunning.value)

async function runBatch() {
  const script = batchScript.value.trim()
  if (!script) {
    ElMessage.warning('请先输入要执行的脚本/命令')
    return
  }
  batchRunning.value = true
  batchResults.value = []
  try {
    const r = await api.batchCommand(
      selected.value.map((n) => n.id),
      'shell',
      { command: script },
    )
    batchResults.value = r.results
    const ok = r.results.filter((x) => x.status === 'ok').length
    ElMessage.success(`完成：${ok}/${r.results.length} 个节点成功`)
  } catch (e: any) {
    ElMessage.error(`批量下发失败：${e?.message || ''}`)
  } finally {
    batchRunning.value = false
  }
}

function closeBatch() {
  batchDialog.value = false
  batchScript.value = ''
  batchResults.value = []
  tableRef.value?.clearSelection()
  selected.value = []
}

function statusLabel(s: string) {
  return { ok: '成功', failed: '失败', offline: '离线', rejected: '被拒' }[s] ?? s
}

// 远程打开节点 1Panel：直连 Tailscale IP（跨网可达），自动带安全入口
function openPanel(n: ApiNode) {
  const ip = n.tailscale_ip
  if (!ip) {
    ElMessage.warning('该节点没有可用的 Tailscale IP，无法远程打开 1Panel')
    return
  }
  const entrance = n.capabilities?.panelEntrance || ''
  const path = entrance ? (entrance.startsWith('/') ? entrance : '/' + entrance) : ''
  window.open(`http://${ip}:31252${path}`, '_blank', 'noopener')
}

function capabilityChips(n: ApiNode) {
  const c = n.capabilities ?? ({} as any)
  return [
    { label: '容器', ok: !!c.canReadDocker, warn: false },
    { label: 'Tailscale', ok: !!c.canManageTailscale, warn: false },
    { label: '终端', ok: !!c.canTerminal, warn: false },
    { label: '网络控制', ok: !!c.canManageNetwork, warn: !c.canManageNetwork },
  ]
}

async function load() {
  loading.value = true
  try {
    const rows = await api.listNodes()
    usingMock.value = false
    nodes.value = rows
  } catch (e: any) {
    usingMock.value = true
    nodes.value = mockNodes as unknown as ApiNode[]
    ElMessage.warning('后端未连接，已展示示例数据')
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div style="display: flex; align-items: center; justify-content: space-between; margin-bottom: 16px">
      <div class="text-secondary" style="font-size: 13px">
        共 {{ nodes.length }} 个节点，{{ onlineCount }} 个在线
        <el-tag v-if="usingMock" size="small" type="warning" style="margin-left: 8px">示例数据</el-tag>
        <el-tag v-if="selectedCount" size="small" type="primary" style="margin-left: 8px">已选 {{ selectedCount }}</el-tag>
      </div>
      <div style="display: flex; gap: 8px">
        <el-button :icon="Promotion" :disabled="!canBatch" @click="batchDialog = true">
          批量指令<span v-if="selectedCount"> ({{ selectedCount }})</span>
        </el-button>
        <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
      </div>
    </div>

    <div class="node-grid">
      <div v-for="node in nodes" :key="node.id" class="node-card">
        <div class="node-card-head">
          <div class="node-name">
            <el-checkbox
              v-model="selected"
              :value="node"
              :disabled="node.status !== 'online'"
              style="margin-right: 8px"
            />
            <span class="status-dot" :class="node.status === 'online' ? 'is-online' : 'is-offline'" />
            {{ node.hostname || node.id }}
          </div>
          <span class="status-tag" :class="node.status === 'online' ? 'is-online' : 'is-offline'">
            {{ node.status === 'online' ? '在线' : '离线' }}
          </span>
        </div>

        <div class="node-ip mono">{{ node.tailscale_ip || '—' }}</div>

        <div class="node-meta">
          <span class="chip">{{ node.arch || '—' }}</span>
          <span class="chip">{{ node.os_version || node.os || '—' }}</span>
          <span class="chip">Agent {{ node.agent_version || '—' }}</span>
        </div>

        <div v-if="node.status === 'online'">
          <div class="metric">
            <div class="metric-label">
              <span>CPU</span>
              <span class="metric-value">{{ node.telemetry.cpuPercent.toFixed(1) }}%</span>
            </div>
            <el-progress :percentage="node.telemetry.cpuPercent" :stroke-width="5" :show-text="false" />
          </div>

          <div class="metric">
            <div class="metric-label">
              <span>内存</span>
              <span class="metric-value">
                {{ formatBytes(node.telemetry.memUsedBytes) }} /
                {{ formatBytes(node.telemetry.memTotalBytes) }}
              </span>
            </div>
            <el-progress
              :percentage="percent(node.telemetry.memUsedBytes, node.telemetry.memTotalBytes)"
              :stroke-width="5"
              :show-text="false"
            />
          </div>

          <div class="metric">
            <div class="metric-label">
              <span>磁盘</span>
              <span class="metric-value">
                {{ formatBytes(node.telemetry.diskUsedBytes) }} /
                {{ formatBytes(node.telemetry.diskTotalBytes) }}
              </span>
            </div>
            <el-progress
              :percentage="percent(node.telemetry.diskUsedBytes, node.telemetry.diskTotalBytes)"
              :stroke-width="5"
              :show-text="false"
            />
          </div>

          <div style="display: flex; gap: 6px; flex-wrap: wrap; margin-top: 12px">
            <span
              v-for="chip in capabilityChips(node)"
              :key="chip.label"
              class="chip"
              :class="chip.ok ? 'is-ok' : chip.warn ? 'is-warn' : ''"
            >
              {{ chip.label }}{{ chip.ok ? '' : ' 需提权' }}
            </span>
          </div>
        </div>

        <div v-else class="text-secondary" style="font-size: 12.5px; padding: 8px 0">
          <div>最后在线：{{ timeAgo(node.last_seen_at) }}</div>
          <div style="display: flex; align-items: center; gap: 4px; margin-top: 6px">
            <el-icon><Timer /></el-icon>
            节点自治运行中，控制面上线后自动补传数据
          </div>
        </div>

        <div class="node-actions">
          <el-button size="small" @click="openPanel(node)">1Panel</el-button>
          <el-button size="small" :disabled="node.status !== 'online'" @click="router.push(`/nodes/${node.id}/containers`)">
            容器
          </el-button>
          <el-button size="small" :disabled="node.status !== 'online'" @click="router.push(`/nodes/${node.id}/vnc`)">
            VNC
          </el-button>
          <el-button size="small" :disabled="node.status !== 'online'" @click="router.push(`/nodes/${node.id}/terminal`)">
            终端
          </el-button>
          <el-button size="small" :disabled="node.status !== 'online'" @click="router.push(`/nodes/${node.id}/files`)">
            文件
          </el-button>
          <el-button size="small" text @click="router.push(`/nodes/${node.id}`)">详情</el-button>
        </div>
      </div>
    </div>

    <!-- 批量指令对话框 -->
    <el-dialog v-model="batchDialog" title="批量下发指令" width="680px" :close-on-click-modal="false" @closed="batchResults = []">
      <div class="text-secondary" style="font-size: 13px; margin-bottom: 10px">
        目标节点：<el-tag v-for="n in selected" :key="n.id" size="small" style="margin-right: 6px">{{ n.hostname || n.id }}</el-tag>
      </div>
      <el-input
        v-model="batchScript"
        type="textarea"
        :rows="7"
        placeholder="输入要批量执行的命令，如：uptime 或多行脚本（每个节点以普通用户身份执行）"
        :disabled="batchRunning"
      />
      <div v-if="batchResults.length" style="margin-top: 14px">
        <div style="font-weight: 600; font-size: 13px; margin-bottom: 8px">执行结果</div>
        <el-table :data="batchResults" size="small" max-height="300">
          <el-table-column label="节点" min-width="120">
            <template #default="{ row }">{{ row.node_id }}</template>
          </el-table-column>
          <el-table-column label="状态" width="90">
            <template #default="{ row }">
              <el-tag :type="row.status === 'ok' ? 'success' : row.status === 'offline' ? 'info' : 'danger'" size="small">
                {{ statusLabel(row.status) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="message" label="信息" min-width="160" show-overflow-tooltip />
          <el-table-column prop="stdout" label="输出（截断）" min-width="200" show-overflow-tooltip>
            <template #default="{ row }">{{ (row.stdout || '').slice(0, 200) }}</template>
          </el-table-column>
        </el-table>
      </div>
      <template #footer>
        <el-button @click="closeBatch">关闭</el-button>
        <el-button type="primary" :loading="batchRunning" :disabled="!batchScript.trim()" @click="runBatch">
          下发执行
        </el-button>
      </template>
    </el-dialog>

    <div class="card" style="margin-top: 20px">
      <div class="card-header">
        <span style="display: flex; align-items: center; gap: 6px">
          <el-icon><Cpu /></el-icon> 关于数据
        </span>
      </div>
      <div class="card-body text-secondary" style="font-size: 13px; line-height: 1.8">
        控制台已接通真实后端接口（控制面不在线时回退示例数据）。节点在控制面离线期间按最后下发的配置自治运行，数据本地缓存后补传。
      </div>
    </div>
  </div>
</template>
