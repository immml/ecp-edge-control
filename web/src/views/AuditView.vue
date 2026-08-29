<script setup lang="ts">
import { ref } from 'vue'

import { mockAuditLogs } from '@/mock/nodes'

const logs = ref(mockAuditLogs)
const keyword = ref('')
</script>

<template>
  <div>
    <div style="margin-bottom: 14px; display: flex; justify-content: space-between">
      <div class="text-secondary" style="font-size: 13px">
        所有下发操作留痕，append-only 不可篡改
      </div>
      <el-input v-model="keyword" placeholder="按操作人或 action 过滤" size="small" style="width: 240px" />
    </div>

    <div class="card">
      <el-table :data="logs" style="width: 100%">
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
