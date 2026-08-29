<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Refresh, View, VideoPlay, VideoPause, RefreshRight } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'

import { api, type ContainerInfo } from '@/api/client'

const route = useRoute()
const router = useRouter()
const nodeId = route.params.id as string

const nodeName = ref(nodeId)
const containers = ref<ContainerInfo[]>([])
const loading = ref(false)
const offline = ref(false)
const busy = ref('') // 正在操作中的容器名（防连点）

// 日志抽屉
const logVisible = ref(false)
const logContent = ref('')
const logLoading = ref(false)
const logContainer = ref('')

async function load() {
  loading.value = true
  offline.value = false
  try {
    const node = await api.getNode(nodeId)
    nodeName.value = (node as any).node?.hostname || nodeId
  } catch { /* 名称拿不到不阻塞 */ }
  try {
    const r = await api.execCommand(nodeId, 'docker_list', {})
    const text = (r as any).stdout || ''
    try {
      containers.value = JSON.parse(text || '[]')
    } catch {
      containers.value = []
      ElMessage.warning('容器列表解析失败（agent 输出非 JSON），请检查 agent 版本')
    }
  } catch (e: any) {
    const msg = e?.message || ''
    if (msg.includes('离线') || msg.includes('offline')) {
      offline.value = true
      containers.value = []
    } else {
      ElMessage.error(`获取容器列表失败：${msg}`)
    }
  } finally {
    loading.value = false
  }
}

// 启停（隔离红线：只有 ecp.managed=true 的容器按钮可用）
async function doAction(c: ContainerInfo, action: 'start' | 'stop' | 'restart') {
  if (busy.value) return
  const verb = action === 'start' ? '启动' : action === 'stop' ? '停止' : '重启'
  try {
    await ElMessageBox.confirm(`确认${verb}容器 ${c.name}？`, '操作确认', {
      type: 'warning',
      confirmButtonText: `${verb}`,
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  busy.value = c.name
  try {
    const r = (await api.execCommand(nodeId, 'docker_action', {
      action,
      container: c.name,
    })) as any
    if (r?.status === 'REJECTED' || r?.status === 'rejected') {
      ElMessage.warning(r?.privilege_hint || r?.message || '操作被拒绝')
    } else if (r?.status === 'FAILED' || r?.status === 'failed') {
      ElMessage.error(r?.message || '操作失败')
    } else {
      ElMessage.success(r?.message || `${verb}成功`)
    }
    await load()
  } catch (e: any) {
    ElMessage.error(`${verb}失败：${e?.message || ''}`)
  } finally {
    busy.value = ''
  }
}

// 日志
async function openLog(c: ContainerInfo) {
  logContainer.value = c.name
  logVisible.value = true
  logContent.value = ''
  logLoading.value = true
  try {
    const r = (await api.execCommand(nodeId, 'docker_logs', { container: c.name, lines: 300 })) as any
    logContent.value = (r?.stdout as string) || '(空日志)'
  } catch (e: any) {
    logContent.value = `获取日志失败：${e?.message || ''}`
  } finally {
    logLoading.value = false
  }
}

function stateTag(state: string): string {
  switch (state) {
    case 'running':
      return 'success'
    case 'exited':
      return 'info'
    case 'restarting':
      return 'warning'
    default:
      return 'danger'
  }
}

onMounted(load)
</script>

<template>
  <div style="display: flex; flex-direction: column; gap: 14px; padding: 16px">
    <div style="display: flex; align-items: center; gap: 10px">
      <el-button size="small" text @click="router.push(`/nodes/${nodeId}`)">← 返回</el-button>
      <el-button size="small" text @click="router.push('/nodes')">节点列表</el-button>
      <span style="font-weight: 600">容器管理</span>
      <span style="color: var(--el-text-color-secondary); font-size: 13px">{{ nodeName }}</span>
      <span style="flex: 1"></span>
      <el-button size="small" :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
    </div>

    <el-alert
      v-if="offline"
      type="error"
      :closable="false"
      show-icon
      title="节点离线"
      description="Agent 未连接，无法读取容器。节点恢复连接后点刷新。"
    />

    <el-table v-loading="loading" :data="containers" style="width: 100%" empty-text="无容器">
      <el-table-column prop="name" label="名称" min-width="180">
        <template #default="{ row }">
          <span>{{ row.name }}</span>
          <el-tag v-if="row.managed === 'true'" size="small" type="warning" style="margin-left: 8px">可管理</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="image" label="镜像" min-width="200" show-overflow-tooltip />
      <el-table-column label="状态" width="110">
        <template #default="{ row }">
          <el-tag :type="stateTag(row.state)" size="small">{{ row.state }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="status" label="详情" min-width="160" show-overflow-tooltip />
      <el-table-column prop="ports" label="端口" min-width="140" show-overflow-tooltip />
      <el-table-column label="操作" width="230" fixed="right">
        <template #default="{ row }">
          <el-button size="small" :icon="View" @click="openLog(row)">日志</el-button>
          <template v-if="row.managed === 'true'">
            <el-button
              v-if="row.state !== 'running'"
              size="small"
              type="success"
              :icon="VideoPlay"
              :loading="busy === row.name"
              @click="doAction(row, 'start')"
            >启动</el-button>
            <el-button
              v-if="row.state === 'running'"
              size="small"
              type="warning"
              :icon="VideoPause"
              :loading="busy === row.name"
              @click="doAction(row, 'stop')"
            >停止</el-button>
            <el-button size="small" :icon="RefreshRight" :loading="busy === row.name" @click="doAction(row, 'restart')">重启</el-button>
          </template>
          <el-tooltip v-else content="隔离红线：仅 ecp.managed=true 的容器可远程管理" placement="top">
            <el-button size="small" disabled>启停</el-button>
          </el-tooltip>
        </template>
      </el-table-column>
    </el-table>

    <el-drawer v-model="logVisible" :title="`容器日志：${logContainer}`" size="55%">
      <pre v-loading="logLoading" style="white-space: pre-wrap; word-break: break-all; font-size: 12px; line-height: 1.6; max-height: 85vh; overflow: auto; margin: 0">{{ logContent }}</pre>
    </el-drawer>
  </div>
</template>
