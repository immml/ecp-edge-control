<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Folder, Document } from '@element-plus/icons-vue'

import { mockNodes } from '@/mock/nodes'
import { formatBytes } from '@/utils/format'
import type { FileEntry } from '@/api/types'

const route = useRoute()
const router = useRouter()
const nodeId = route.params.id as string
const node = mockNodes.find((n) => n.id === nodeId)

const currentPath = ref('/opt/ecp-agent')

// mock 文件列表。真实实现走 GET /api/v1/nodes/{id}/files?path=...
const entries = ref<FileEntry[]>([
  { name: 'bin', path: '/opt/ecp-agent/bin', isDir: true, size: 4096, mode: 'drwxr-xr-x', modifiedAt: '2026-08-29T16:10:00+08:00' },
  { name: 'cache.db', path: '/opt/ecp-agent/cache.db', isDir: false, size: 98_304, mode: '-rw-------', modifiedAt: '2026-08-29T17:30:00+08:00' },
  { name: 'logs', path: '/opt/ecp-agent/logs', isDir: true, size: 4096, mode: 'drwxr-xr-x', modifiedAt: '2026-08-29T17:29:00+08:00' },
  { name: 'agent.yaml', path: '/opt/ecp-agent/agent.yaml', isDir: false, size: 742, mode: '-rw-r--r--', modifiedAt: '2026-08-29T17:12:00+08:00' },
])

function open(entry: FileEntry) {
  if (entry.isDir) currentPath.value = entry.path
}
</script>

<template>
  <div style="display: flex; flex-direction: column; gap: 12px; height: 100%">
    <div style="display: flex; align-items: center; gap: 10px">
      <el-button size="small" text @click="router.push('/nodes')">← 返回</el-button>
      <span style="font-weight: 600">{{ node?.hostname ?? nodeId }}</span>
      <span class="chip mono">{{ currentPath }}</span>
    </div>

    <div class="card" style="flex: 1; min-height: 0; display: flex; flex-direction: column">
      <div class="card-header">
        <span>文件</span>
        <div style="display: flex; gap: 8px">
          <el-button size="small">上传</el-button>
          <el-button size="small">新建</el-button>
        </div>
      </div>

      <el-table :data="entries" style="width: 100%" @row-click="open">
        <el-table-column label="名称" min-width="240">
          <template #default="{ row }">
            <div style="display: flex; align-items: center; gap: 8px; cursor: pointer">
              <el-icon :color="row.isDir ? '#6b37c9' : '#9aa0a6'">
                <component :is="row.isDir ? Folder : Document" />
              </el-icon>
              <span>{{ row.name }}</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="大小" width="120">
          <template #default="{ row }">
            <span class="mono text-secondary">{{ row.isDir ? '—' : formatBytes(row.size, 1) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="权限" width="140">
          <template #default="{ row }">
            <span class="mono text-secondary">{{ row.mode }}</span>
          </template>
        </el-table-column>
        <el-table-column label="修改时间" width="200">
          <template #default="{ row }">
            <span class="text-secondary">{{ new Date(row.modifiedAt).toLocaleString('zh-CN') }}</span>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>
