<script setup lang="ts">
import { useRoute, useRouter } from 'vue-router'

import { mockContainers, mockNodes } from '@/mock/nodes'
import { formatBytes, percent, timeAgo } from '@/utils/format'

const route = useRoute()
const router = useRouter()
const nodeId = route.params.id as string
const node = mockNodes.find((n) => n.id === nodeId)

const caps = node?.capabilities
</script>

<template>
  <div v-if="node" style="display: flex; flex-direction: column; gap: 16px">
    <div style="display: flex; align-items: center; gap: 10px">
      <el-button size="small" text @click="router.push('/nodes')">← 返回</el-button>
      <span style="font-weight: 600">{{ node.hostname }}</span>
      <span class="status-tag" :class="node.status === 'online' ? 'is-online' : 'is-offline'">
        {{ node.status === 'online' ? '在线' : '离线' }}
      </span>
    </div>

    <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 16px">
      <div class="card">
        <div class="card-header">系统信息</div>
        <div class="card-body">
          <el-descriptions :column="1" size="small" border>
            <el-descriptions-item label="主机名">{{ node.hostname }}</el-descriptions-item>
            <el-descriptions-item label="Tailscale IP">
              <span class="mono">{{ node.tailscaleIp }}</span>
            </el-descriptions-item>
            <el-descriptions-item label="架构">{{ node.arch }}</el-descriptions-item>
            <el-descriptions-item label="系统">{{ node.osVersion }}</el-descriptions-item>
            <el-descriptions-item label="内核">
              <span class="mono">{{ node.kernel }}</span>
            </el-descriptions-item>
            <el-descriptions-item label="Agent 版本">{{ node.agentVersion }}</el-descriptions-item>
            <el-descriptions-item label="最后在线">{{ timeAgo(node.lastSeenAt) }}</el-descriptions-item>
          </el-descriptions>
        </div>
      </div>

      <div class="card">
        <div class="card-header">资源占用</div>
        <div class="card-body">
          <div class="metric">
            <div class="metric-label">
              <span>CPU</span><span class="metric-value">{{ node.telemetry.cpuPercent.toFixed(1) }}%</span>
            </div>
            <el-progress :percentage="node.telemetry.cpuPercent" :stroke-width="6" :show-text="false" />
          </div>
          <div class="metric">
            <div class="metric-label">
              <span>内存</span>
              <span class="metric-value">
                {{ formatBytes(node.telemetry.memUsedBytes) }} / {{ formatBytes(node.telemetry.memTotalBytes) }}
              </span>
            </div>
            <el-progress
              :percentage="percent(node.telemetry.memUsedBytes, node.telemetry.memTotalBytes)"
              :stroke-width="6"
              :show-text="false"
            />
          </div>
          <div class="metric">
            <div class="metric-label">
              <span>磁盘</span>
              <span class="metric-value">
                {{ formatBytes(node.telemetry.diskUsedBytes) }} / {{ formatBytes(node.telemetry.diskTotalBytes) }}
              </span>
            </div>
            <el-progress
              :percentage="percent(node.telemetry.diskUsedBytes, node.telemetry.diskTotalBytes)"
              :stroke-width="6"
              :show-text="false"
            />
          </div>
          <div style="display: flex; gap: 20px; margin-top: 14px; font-size: 12.5px">
            <div>
              <div class="text-secondary">负载 (1m)</div>
              <div class="mono" style="font-size: 15px">{{ node.telemetry.load1 }}</div>
            </div>
            <div>
              <div class="text-secondary">温度</div>
              <div class="mono" style="font-size: 15px">{{ node.telemetry.temperatureCelsius }}°C</div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div class="card">
      <div class="card-header">
        <span>能力探测</span>
        <span class="text-secondary" style="font-weight: 400; font-size: 12px">
          以 {{ caps?.runAsUser }} (uid {{ caps?.runAsUid }}) 运行
        </span>
      </div>
      <div class="card-body" style="display: flex; gap: 8px; flex-wrap: wrap">
        <span class="chip" :class="caps?.canReadSystemStats ? 'is-ok' : 'is-warn'">系统采集</span>
        <span class="chip" :class="caps?.canTerminal ? 'is-ok' : 'is-warn'">终端</span>
        <span class="chip" :class="caps?.canManageFiles ? 'is-ok' : 'is-warn'">文件管理</span>
        <span class="chip" :class="caps?.canReadDocker ? 'is-ok' : 'is-warn'">容器只读</span>
        <span class="chip" :class="caps?.canWriteDocker ? 'is-ok' : 'is-warn'">容器写操作</span>
        <span class="chip" :class="caps?.canManageTailscale ? 'is-ok' : 'is-warn'">Tailscale 纳管</span>
        <span class="chip" :class="caps?.canManageNetwork ? 'is-ok' : 'is-warn'">网络控制</span>
        <span class="chip" :class="caps?.canManageSystemd ? 'is-ok' : 'is-warn'">systemd</span>
        <span class="chip" :class="caps?.canSelfUpgrade ? 'is-ok' : 'is-warn'">自升级</span>
      </div>
    </div>

    <div class="card">
      <div class="card-header">
        <span>容器</span>
        <span class="text-secondary" style="font-weight: 400; font-size: 12px">
          仅对 ECP 纳管的容器执行写操作
        </span>
      </div>
      <el-table :data="mockContainers" style="width: 100%">
        <el-table-column prop="name" label="名称" min-width="160" />
        <el-table-column prop="image" label="镜像" min-width="220">
          <template #default="{ row }"><span class="mono text-secondary">{{ row.image }}</span></template>
        </el-table-column>
        <el-table-column prop="status" label="状态" width="180" />
        <el-table-column label="归属" width="140">
          <template #default="{ row }">
            <span class="chip" :class="row.managed ? 'is-ok' : ''">
              {{ row.managed ? 'ECP 纳管' : '节点既有业务' }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="120">
          <template #default="{ row }">
            <el-button size="small" text :disabled="!row.managed">
              {{ row.managed ? '停止' : '不可操作' }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>

  <el-empty v-else description="节点不存在" />
</template>
