<script setup lang="ts">
// 网络管理（节点维度）：选择一个节点后，对该节点做组网（Tailscale/FRP）、
// 网络管理（WiFi/网卡/测速/虚拟MAC）、VPN 跳板（Clash 导出）。
import { computed, onMounted, ref } from 'vue'
import { Refresh, Download } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'

import { api, type ApiNode } from '@/api/client'
import NetworkPanel from '@/components/NetworkPanel.vue'
import NodeMeshPanel from '@/components/NodeMeshPanel.vue'

const activeTab = ref('net')
const nodes = ref<ApiNode[]>([])
const selectedId = ref('')
const selected = computed<ApiNode | null>(() => nodes.value.find((n) => n.id === selectedId.value) ?? null)

onMounted(async () => {
  try {
    nodes.value = (await api.listNodes()).filter((n) => n.status === 'online')
    if (!selectedId.value && nodes.value.length) selectedId.value = nodes.value[0].id
  } catch (e: any) {
    ElMessage.error(`加载节点失败：${e?.message || ''}`)
  }
})

// —— VPN / Clash 多跳板 ——
const vpnNodes = ref<{ name: string; server: string; port: number; uuid: string; path: string }[]>([])
const vpnBusy = ref(false)

function addVpnNode() {
  vpnNodes.value.push({ name: '', server: '', port: 443, uuid: '', path: '/ecp-vpn' })
}
function removeVpnNode(i: number) {
  vpnNodes.value.splice(i, 1)
}
/** 用当前在线节点快速填充跳板列表（每节点一行）。 */
function fillFromNodes() {
  const online = nodes.value.filter((n) => n.tailscale_ip)
  if (!online.length) {
    ElMessage.warning('没有带 Tailscale IP 的在线节点可填充')
    return
  }
  vpnNodes.value = online.map((n) => ({
    name: n.hostname || n.id,
    server: `${n.hostname || n.id}.vpn.YOUR_DOMAIN`,
    port: 443,
    uuid: '',
    path: '/ecp-vpn',
  }))
  ElMessage.success(`已填充 ${online.length} 个跳板，请补 UUID 与真实域名`)
}
async function exportClash() {
  const valid = vpnNodes.value.filter((n) => n.server && n.uuid)
  if (!valid.length) {
    ElMessage.warning('请至少填写一个跳板的域名与 UUID（可点「从在线节点填充」）')
    return
  }
  vpnBusy.value = true
  try {
    const yaml = await api.exportClash({ nodes: valid })
    const blob = new Blob([yaml], { type: 'application/x-yaml' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `clash-ecp-${new Date().toISOString().slice(0, 10)}.yaml`
    a.click()
    URL.revokeObjectURL(a.href)
    ElMessage.success(`已导出 ${valid.length} 个跳板的 Clash 配置`)
  } catch (e: any) {
    ElMessage.error(`导出失败：${e?.message || ''}`)
  } finally {
    vpnBusy.value = false
  }
}
</script>

<template>
  <div style="display: flex; flex-direction: column; gap: 14px">
    <!-- 节点选择器：所有操作都是针对选定节点 -->
    <div style="display: flex; align-items: center; gap: 10px; flex-wrap: wrap">
      <span class="text-secondary" style="font-size: 13px">操作节点：</span>
      <el-select v-model="selectedId" filterable style="width: 300px" placeholder="选择在线节点">
        <el-option v-for="n in nodes" :key="n.id" :value="n.id" :label="`${n.hostname || n.id}（${n.id}）`">
          <span>{{ n.hostname || n.id }}</span>
          <span class="mono text-secondary" style="font-size: 12px; float: right">{{ n.id }}</span>
        </el-option>
      </el-select>
      <el-button :icon="Refresh" @click="onMounted">刷新</el-button>
    </div>

    <el-alert v-if="!nodes.length" type="info" :closable="false" show-icon
      title="当前没有在线节点" description="网络管理 / 组网 / VPN 跳板均按节点操作，请先确保至少一个节点在线。" />

    <el-tabs v-model="activeTab" v-else>
      <!-- 组网：Tailscale / FRP（选定节点） -->
      <el-tab-pane label="组网（Tailscale / FRP）" name="mesh">
        <NodeMeshPanel v-if="selected" :node="selected" />
      </el-tab-pane>

      <!-- 网络管理：WiFi / 网卡 / 测速 / 虚拟MAC（选定节点） -->
      <el-tab-pane label="网络管理（WiFi / 网卡 / 测速 / 虚拟MAC）" name="net">
        <NetworkPanel v-if="selected" :node="selected" />
      </el-tab-pane>

      <!-- VPN / Clash 多跳板：每个盒子是一个独立出口 -->
      <el-tab-pane label="VPN 跳板（Clash）" name="vpn">
        <div class="text-secondary" style="font-size: 13px; margin-bottom: 10px">
          <b>每个边缘盒子 = 一个独立 VPN 出口（跳板）。</b>
          在节点上执行 <code class="mono">sudo bash deploy/xray-vpn.sh</code> 部署 xray（VMess+WS 回环监听 127.0.0.1:8444），
          再为每个盒子建一条 Cloudflare Tunnel（<code class="mono">node1.vpn.你的域名 → http://127.0.0.1:8444</code>）。
          导出的 Clash 配置包含全部跳板，可任选其一访问内网。
        </div>
        <el-card shadow="never">
          <div class="card-header">
            <span>跳板列表（每节点一行）</span>
            <div>
              <el-button size="small" @click="addVpnNode">+ 手动添加</el-button>
              <el-button size="small" type="primary" plain @click="fillFromNodes">从在线节点填充</el-button>
            </div>
          </div>
          <div v-for="(vn, i) in vpnNodes" :key="i"
            style="border: 1px solid var(--el-border-color-lighter); border-radius: 8px; padding: 12px; margin-bottom: 10px">
            <div style="display: flex; align-items: center; gap: 8px; margin-bottom: 8px">
              <b style="font-size: 13px">跳板 #{{ i + 1 }}</b>
              <span style="flex: 1"></span>
              <el-button size="small" text type="danger" @click="removeVpnNode(i)">删除</el-button>
            </div>
            <el-form label-width="96px" size="small" label-position="left">
              <el-form-item label="节点名">
                <el-input v-model="vn.name" placeholder="如 orangepi3b（Clash 中的节点名）" />
              </el-form-item>
              <el-form-item label="入口域名">
                <el-input v-model="vn.server" placeholder="node1.vpn.yourdomain.com（Cloudflare Tunnel Hostname）" />
              </el-form-item>
              <el-form-item label="端口">
                <el-input-number v-model="vn.port" :min="1" :max="65535" style="width: 140px" />
              </el-form-item>
              <el-form-item label="UUID">
                <el-input v-model="vn.uuid" placeholder="xray 生成的 UUID" />
              </el-form-item>
              <el-form-item label="WS 路径">
                <el-input v-model="vn.path" placeholder="/ecp-vpn" />
              </el-form-item>
            </el-form>
          </div>
          <el-empty v-if="!vpnNodes.length" description="尚未添加跳板" :image-size="50" />
          <div style="display: flex; gap: 10px; margin-top: 12px">
            <el-button type="primary" :loading="vpnBusy" @click="exportClash">
              <el-icon style="margin-right: 4px"><Download /></el-icon>导出 Clash 配置（全部跳板）
            </el-button>
            <span class="text-secondary" style="font-size: 12px; align-self: center">
              默认内网段：192.168/16、172.16/12、10/8、100.64/10（tailnet）
            </span>
          </div>
        </el-card>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>