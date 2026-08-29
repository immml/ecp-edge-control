<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'

import { api } from '@/api/client'
import { mockAuditLogs } from '@/mock/nodes'

const logs = ref<any[]>([])
const keyword = ref('')
const usingMock = ref(false)

async function load() {
  try {
    const r = await api.audit()
    usingMock.value = false
    logs.value = r.logs
  } catch {
    usingMock.value = true
    logs.value = mockAuditLogs
    ElMessage.warning('后端未连接，已展示示例数据')
  }
}

const filtered = computed(() => {
  const k = keyword.value.trim().toLowerCase()
  if (!k) return logs.value
  return logs.value.filter(
    (l) =>
      (l.username || '').toLowerCase().includes(k) || (l.action || '').toLowerCase().includes(k),
  )
})

onMounted(load)
</script>

<template>
  <div>
    <div style="margin-bottom: 14px; display: flex; justify-content: space-between; align-items: center">
      <div class="text-secondary" style="font-size: 13px">
        所有下发操作留痕，append-only 不可篡改
        <el-tag v-if="usingMock" size="small" type="warning" style="margin-left: 8px">示例数据</el-tag>
      </div>
      <el-input v-model="keyword" placeholder="按操作人或 action 过滤" size="small" style="width: 240px" />
    </div>

    <div class="card">
      <el-table :data="filtered" style="width: 100%">
        <el-table-column label="时间" width="190">
          <template #default="{ row }">
            <span class="mono text-secondary">{{ new Date(row.ts).toLocaleString('zh-CN') }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="username" label="操作人" width="110" />
        <el-table-column prop="nodeId" label="节点" width="160">
          <template #default="{ row }"><span class="mono">{{ row.nodeId }}</span></template>
        </el-table-column>
        <el-table-column prop="action" label="操作" width="200">
          <template #default="{ row }"><span class="mono">{{ row.action }}</span></template>
        </el-table-column>
        <el-table-column prop="detail" label="说明" min-width="260">
          <template #default="{ row }"><span class="text-secondary">{{ row.detail }}</span></template>
        </el-table-column>
        <el-table-column label="结果" width="90">
          <template #default="{ row }">
            <span class="chip is-ok">{{ row.result }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="traceId" label="Trace" width="110">
          <template #default="{ row }"><span class="mono text-secondary">{{ row.traceId }}</span></template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>
