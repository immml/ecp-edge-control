<script setup lang="ts">
// VPN 跳板汇总页：读取各节点详情页里保存的跳板配置（localStorage），
// 汇总导出 Clash 配置。每个跳板对应一个边缘盒子（独立出口）。
// 单节点的部署/配置请到「节点详情 → VPN 跳板」操作。
import { onMounted, ref } from 'vue'
import { Download } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'

import { api, type ApiNode } from '@/api/client'

const STORE_KEY = 'ecp.vpn.jumps'
interface JumpConfig { name: string; server: string; port: number; uuid: string; path: string }

const nodes = ref<ApiNode[]>([])
const jumps = ref<JumpConfig[]>([])
const extraIps = ref('')
const vpnBusy = ref(false)

function listJumps(): Record<string, JumpConfig> {
  try { return JSON.parse(localStorage.getItem(STORE_KEY) || '{}') } catch { return {} }
}

onMounted(async () => {
  jumps.value = Object.values(listJumps())
  try {
    nodes.value = (await api.listNodes()).filter((n) => n.status === 'online')
  } catch { /* keep empty */ }
})

function refresh() {
  jumps.value = Object.values(listJumps())
}

function removeJump(i: number) {
  // 从 storage 中删除对应条目（name 匹配）
  const all = listJumps()
  const target = jumps.value[i]
  for (const k of Object.keys(all)) {
    if (all[k].name === target.name && all[k].server === target.server) delete all[k]
  }
  localStorage.setItem(STORE_KEY, JSON.stringify(all))
  refresh()
}

async function exportClash() {
  if (!jumps.value.length) {
    ElMessage.warning('还没有已保存的跳板。请到各节点详情 →「VPN 跳板」填写并保存配置。')
    return
  }
  vpnBusy.value = true
  try {
    const yaml = await api.exportClash({ nodes: jumps.value, extra_ips: extraIps.value })
    const blob = new Blob([yaml], { type: 'application/x-yaml' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `clash-ecp-${new Date().toISOString().slice(0, 10)}.yaml`
    a.click()
    URL.revokeObjectURL(a.href)
    ElMessage.success(`已导出 ${jumps.value.length} 个跳板（Clash 客户端导入后任选出口访问内网）`)
  } catch (e: any) {
    ElMessage.error(`导出失败：${e?.message || ''}`)
  } finally {
    vpnBusy.value = false
  }
}
</script>

<template>
  <div style="display: flex; flex-direction: column; gap: 14px">
    <el-alert type="info" :closable="false" show-icon
      title="每个边缘盒子 = 一个独立 VPN 跳板"
      description="跳板配置在「节点详情 → VPN 跳板」里按节点填写保存（部署 xray-vpn.sh + Cloudflare Tunnel 后填入 UUID/域名）。本页汇总所有已保存的跳板，导出 Clash 配置后任选出口访问内网。" />

    <el-card shadow="never">
      <div class="card-header">
        <span>已保存跳板（{{ jumps.length }}）</span>
        <div>
          <el-button size="small" @click="refresh">刷新</el-button>
          <el-button size="small" type="primary" :loading="vpnBusy" @click="exportClash">
            <el-icon style="margin-right: 4px"><Download /></el-icon>导出 Clash 配置
          </el-button>
        </div>
      </div>
      <el-table :data="jumps" size="small" v-if="jumps.length">
        <el-table-column prop="name" label="节点名" min-width="120" />
        <el-table-column prop="server" label="入口域名" min-width="220">
          <template #default="{ row }"><span class="mono">{{ row.server }}</span></template>
        </el-table-column>
        <el-table-column prop="port" label="端口" width="70" />
        <el-table-column prop="path" label="WS 路径" width="110" />
        <el-table-column prop="uuid" label="UUID" min-width="240">
          <template #default="{ row }"><span class="mono" style="font-size:12px">{{ row.uuid }}</span></template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ $index }">
            <el-button size="small" text type="danger" @click="removeJump($index)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-empty v-else description="暂无已保存的跳板 → 去某个在线节点详情 →「VPN 跳板」填写" :image-size="60" />

      <el-form v-if="jumps.length" label-width="96px" size="small" style="margin-top: 12px">
        <el-form-item label="附加网段">
          <el-input v-model="extraIps" placeholder="可选，逗号分隔；默认已含 10/8、172.16/12、192.168/16、100.64/10（tailnet）" />
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>