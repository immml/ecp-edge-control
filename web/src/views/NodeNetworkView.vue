<script setup lang="ts">
// 节点维度网络管理页：组网（Tailscale/FRP）+ 网络管理（WiFi/网卡/测速/虚拟MAC）+ VPN 跳板。
// 从 NetworkView 演化：固定操作某一节点，供 /nodes/:id/network 使用。
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import type { ApiNode } from '@/api/client'

import { api } from '@/api/client'
import NetworkPanel from '@/components/NetworkPanel.vue'
import NodeMeshPanel from '@/components/NodeMeshPanel.vue'

const route = useRoute()
const router = useRouter()
const nodeId = route.params.id as string

const activeTab = ref('mesh')
const node = ref<ApiNode | null>(null)
const loaded = ref(false)

async function load() {
  try {
    const d = await api.getNode(nodeId)
    node.value = { ...(d as any).node, capabilities: (d as any).view?.capabilities ?? (d as any).node?.capabilities }
  } catch (e: any) {
    ElMessage.error(`加载节点失败：${e?.message || ''}`)
  } finally {
    loaded.value = true
  }
}
onMounted(load)
</script>

<template>
  <div style="display: flex; flex-direction: column; gap: 14px">
    <div style="display: flex; align-items: center; gap: 10px">
      <el-button size="small" text @click="router.push(`/nodes/${nodeId}`)">← 返回节点</el-button>
      <b>{{ node?.hostname || nodeId }}</b>
      <span class="mono text-secondary" style="font-size: 12px">{{ nodeId }}</span>
      <span style="flex: 1"></span>
      <el-button size="small" :icon="'Refresh'" @click="load">刷新</el-button>
    </div>

    <el-tabs v-model="activeTab" v-if="node">
      <el-tab-pane label="组网（Tailscale / FRP）" name="mesh">
        <NodeMeshPanel :node="node" />
      </el-tab-pane>
      <el-tab-pane label="网络管理（WiFi / 网卡 / 测速 / 虚拟MAC）" name="net">
        <NetworkPanel :node="node" />
      </el-tab-pane>
      <el-tab-pane label="VPN 跳板（Clash）" name="vpn">
        <div class="text-secondary" style="font-size: 13px; margin-bottom: 10px">
          <b>每个边缘盒子 = 一个独立 VPN 出口（跳板）。</b>
          <ol style="margin: 8px 0; padding-left: 20px; line-height: 1.8">
            <li>在本节点执行 <code class="mono">sudo bash deploy/xray-vpn.sh</code> 部署 xray（VMess+WS，回环监听 127.0.0.1:8444）；</li>
            <li>为它建一条 Cloudflare Tunnel：<code class="mono">{{ node.hostname || node.id }}.vpn.你的域名 → http://127.0.0.1:8444</code>；</li>
            <li>把域名与 UUID 填到「组网管理 → VPN 跳板」页，导出 Clash 后即可把本盒子当出口访问内网。</li>
          </ol>
          <el-button type="primary" plain size="small" @click="router.push('/network')">去组网管理页配置跳板并导出 Clash</el-button>
        </div>
      </el-tab-pane>
    </el-tabs>
    <el-empty v-else-if="loaded" description="节点加载失败或不存在" />
  </div>
</template>