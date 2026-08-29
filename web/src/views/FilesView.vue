<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Folder, Document, Refresh, Back } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'

import { api } from '@/api/client'
import { formatBytes } from '@/utils/format'

interface RemoteFile {
  name: string
  path: string
  is_dir: boolean
  size: number
  mode: string
  modified_at: string
}

const route = useRoute()
const router = useRouter()
const nodeId = route.params.id as string

const nodeName = ref(nodeId)
const currentPath = ref('/')
const entries = ref<RemoteFile[]>([])
const loading = ref(false)
const offline = ref(false)

function normalizePath(p: string): string {
  const parts = p.split('/').filter(Boolean)
  return '/' + parts.join('/')
}

async function load(path?: string) {
  if (path !== undefined) currentPath.value = normalizePath(path)
  loading.value = true
  offline.value = false
  try {
    const node = await api.getNode(nodeId)
    nodeName.value = (node as any).node?.hostname || nodeId
  } catch {
    /* 名称拿不到不阻塞 */
  }
  try {
    const r = await api.listFiles(nodeId, currentPath.value)
    entries.value = r.items
  } catch (e: any) {
    entries.value = []
    if (/离线/.test(e?.message ?? '')) {
      offline.value = true
      ElMessage.warning('节点离线，无法读取文件')
    } else {
      ElMessage.error(`读取目录失败：${e?.message ?? '未知错误'}`)
    }
  } finally {
    loading.value = false
  }
}

function open(item: RemoteFile) {
  if (item.is_dir) load(item.path)
}

function goUp() {
  if (currentPath.value === '/') return
  const parent = currentPath.value.split('/').slice(0, -1).join('/') || '/'
  load(parent)
}

function openFile(item: RemoteFile) {
  if (item.is_dir) return
  ElMessage.info('文件内容读取（FILE_READ）将在下一版提供')
}

onMounted(() => load())
</script>

<template>
  <div style="display: flex; flex-direction: column; gap: 12px; height: 100%">
    <div style="display: flex; align-items: center; gap: 10px">
      <el-button size="small" text @click="router.push('/nodes')">← 返回</el-button>
      <span style="font-weight: 600">{{ nodeName }}</span>
      <span class="chip mono">{{ currentPath }}</span>
      <span style="flex: 1"></span>
      <el-button size="small" :icon="Refresh" :loading="loading" @click="load()">刷新</el-button>
    </div>

    <div class="card" style="flex: 1; min-height: 0; display: flex; flex-direction: column">
      <div class="card-header">
        <span>文件（{{ currentPath }}）</span>
        <div style="display: flex; gap: 8px">
          <el-button size="small" :icon="Back" :disabled="currentPath === '/'" @click="goUp">上级</el-button>
          <el-button size="small" disabled>上传</el-button>
          <el-button size="small" disabled>新建</el-button>
        </div>
      </div>

      <el-table
        v-loading="loading"
        :data="entries"
        style="width: 100%"
        @row-click="open"
      >
        <el-table-column label="名称" min-width="260">
          <template #default="{ row }">
            <div style="display: flex; align-items: center; gap: 8px; cursor: pointer">
              <el-icon :color="row.is_dir ? '#6b37c9' : '#9aa0a6'">
                <component :is="row.is_dir ? Folder : Document" />
              </el-icon>
              <span>{{ row.name }}</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="大小" width="120">
          <template #default="{ row }">
            <span class="mono text-secondary">{{ row.is_dir ? '—' : formatBytes(row.size, 1) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="权限" width="140">
          <template #default="{ row }">
            <span class="mono text-secondary">{{ row.mode }}</span>
          </template>
        </el-table-column>
        <el-table-column label="修改时间" width="200">
          <template #default="{ row }">
            <span class="text-secondary">{{ new Date(row.modified_at).toLocaleString('zh-CN') }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="90">
          <template #default="{ row }">
            <el-button size="small" text :disabled="row.is_dir" @click="openFile(row)">查看</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div v-if="offline" class="card-body text-secondary" style="font-size: 13px; padding: 12px">
        节点当前离线。文件读取需要节点在线（Agent 直连执行），请确认节点网络后重试。
      </div>
    </div>
  </div>
</template>
