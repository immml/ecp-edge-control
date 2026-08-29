<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Monitor, Cpu, Timer } from '@element-plus/icons-vue'

import { mockNodes } from '@/mock/nodes'
import { formatBytes, percent, timeAgo } from '@/utils/format'
import type { Node } from '@/api/types'

const router = useRouter()
const nodes = ref<Node[]>(mockNodes)

// 能力摘要：把 10 项能力压成几个关键 chip，避免卡片被标签淹没
function capabilityChips(node: Node) {
  const c = node.capabilities
  return [
    { label: '容器', ok: c.canReadDocker, warn: false },
    { label: 'Tailscale', ok: c.canManageTailscale, warn: false },
    { label: '终端', ok: c.canTerminal, warn: false },
    { label: '网络控制', ok: c.canManageNetwork, warn: !c.canManageNetwork },
  ]
}

const onlineCount = computed(() => nodes.value.filter((n) => n.status === 'online').length)
</script>

<template>
  <div>
    <div style="display: flex; align-items: center; justify-content: space-between; margin-bottom: 16px">
      <div class="text-secondary" style="font-size: 13px">
        共 {{ nodes.length }} 个节点，{{ onlineCount }} 个在线
      </div>
      <el-button type="primary" :icon="Monitor">纳管新节点</el-button>
    </div>

    <div class="node-grid">
      <div v-for="node in nodes" :key="node.id" class="node-card">
        <div class="node-card-head">
          <div class="node-name">
            <span class="status-dot" :class="node.status === 'online' ? 'is-online' : 'is-offline'" />
            {{ node.hostname }}
          </div>
          <span class="status-tag" :class="node.status === 'online' ? 'is-online' : 'is-offline'">
            {{ node.status === 'online' ? '在线' : '离线' }}
          </span>
        </div>

        <div class="node-ip mono">{{ node.tailscaleIp }}</div>

        <div class="node-meta">
          <span class="chip">{{ node.arch }}</span>
          <span class="chip">{{ node.osVersion }}</span>
          <span class="chip">Agent {{ node.agentVersion }}</span>
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
          <div>最后在线：{{ timeAgo(node.lastSeenAt) }}</div>
          <div style="display: flex; align-items: center; gap: 4px; margin-top: 6px">
            <el-icon><Timer /></el-icon>
            节点自治运行中，控制面上线后自动补传数据
          </div>
        </div>

        <div class="node-actions">
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

    <div class="card" style="margin-top: 20px">
      <div class="card-header">
        <span style="display: flex; align-items: center; gap: 6px">
          <el-icon><Cpu /></el-icon> 关于数据
        </span>
      </div>
      <div class="card-body text-secondary" style="font-size: 13px; line-height: 1.8">
        当前展示的是 mock 数据，数值取自 Orange Pi 3B 真机实测（内存 3.8 GiB、磁盘 232G、负载 8.11、温度 56.1°C）。
        控制面按需上线，节点在控制面离线期间按最后下发的配置自治运行，数据本地缓存后补传。
      </div>
    </div>
  </div>
</template>
