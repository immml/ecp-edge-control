<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'

import { api, type ApiNode, type CommandResult } from '@/api/client'

const nodes = ref<ApiNode[]>([])
const loading = ref(false)
const busy = ref<string>('') // nodeId:action，防连点

interface NetState {
  tailscale: { loaded: boolean; ok: boolean; summary: string }
  frp: { loaded: boolean; ok: boolean; summary: string; configured: boolean; running: boolean }
}
const states = ref<Record<string, NetState>>({})

function stateOf(nodeId: string): NetState {
  if (!states.value[nodeId]) {
    states.value[nodeId] = {
      tailscale: { loaded: false, ok: false, summary: '—' },
      frp: { loaded: false, ok: false, summary: '—', configured: false, running: false },
    }
  }
  return states.value[nodeId]
}

async function load() {
  loading.value = true
  try {
    const rows = await api.listNodes()
    nodes.value = rows.filter((n) => n.status === 'online')
    // 并行拉取每台节点的 Tailscale + FRP 状态
    const jobs: Promise<void>[] = []
    for (const n of nodes.value) {
      jobs.push(loadNodeState(n))
    }
    await Promise.all(jobs)
  } catch (e: any) {
    ElMessage.error(`加载失败：${e?.message || ''}`)
  } finally {
    loading.value = false
  }
}

async function loadNodeState(n: ApiNode) {
  const s = stateOf(n.id)
  // Tailscale
  try {
    const r = await api.execCommand(n.id, 'tailscale_status', {})
    s.tailscale.loaded = true
    s.tailscale.ok = String(r.status).includes('OK')
    s.tailscale.summary = summarize(r.stdout, 6)
  } catch {
    s.tailscale.loaded = true
    s.tailscale.summary = '查询失败（可能未安装）'
  }
  // FRP
  try {
    const r = await api.execCommand(n.id, 'frp_status', {})
    s.frp.loaded = true
    s.frp.ok = true
    try {
      const j = JSON.parse(r.stdout || '{}')
      s.frp.configured = !!j.configured
      s.frp.running = !!j.running
      s.frp.summary = j.bin
        ? `frpc ${j.running ? '运行中' : '未运行'} · 配置:${j.config || '无'}`
        : 'frpc 未安装'
    } catch {
      s.frp.summary = (r.stdout || '—').slice(0, 120)
    }
  } catch {
    s.frp.loaded = true
    s.frp.summary = '查询失败'
  }
}

function summarize(stdout: string, lines: number): string {
  const t = (stdout || '').trim()
  if (!t) return '(空输出)'
  const ls = t.split('\n').slice(0, lines)
  if (ls.length === 0) return '(空输出)'
  // tailscale status 首行一般是 "Logged out." 或 100.x 设备表，取有意义的行
  return ls.join('\n')
}

async function doAction(n: ApiNode, kind: 'tailscale' | 'frp', action: 'up' | 'down') {
  const key = `${n.id}:${kind}:${action}`
  if (busy.value) return
  busy.value = key
  const label = `${kind === 'tailscale' ? 'Tailscale' : 'frpc'} ${action === 'up' ? '启用' : '停用'}`
  try {
    const type = kind === 'tailscale' ? `tailscale_${action}` : `frp_${action}`
    const r = await api.execCommand(n.id, type, {})
    if (String(r.status).includes('NEEDS_PRIVILEGE')) {
      // 架构降级：展示脚本交人工 sudo（节点以普通用户运行，绝不自行提权）
      await showPrivilegeDialog(n, label, r)
    } else if (String(r.status).includes('OK')) {
      ElMessage.success(`${n.hostname || n.id} ${label}成功`)
    } else {
      ElMessage.error(`${n.hostname || n.id} ${label}失败：${r.message || ''}`)
    }
    await loadNodeState(n)
  } catch (e: any) {
    ElMessage.error(`${label}失败：${e?.message || ''}`)
  } finally {
    busy.value = ''
  }
}

async function showPrivilegeDialog(n: ApiNode, label: string, r: CommandResult) {
  const script = r.privilege_script || ''
  try {
    await ElMessageBox.confirm(
      `<div style="font-size:13px;line-height:1.7">
         <p style="margin:0 0 8px">${r.privilege_hint || '该操作需要 root 权限'}（节点以普通用户运行，平台不自行提权）。</p>
         <p style="margin:0 0 6px">请在节点 <b>${n.hostname || n.id}</b> 上执行：</p>
         <pre style="background:var(--el-fill-color-light);padding:10px;border-radius:6px;overflow:auto;font-size:12px;white-space:pre-wrap">${script.replace(/</g, '&lt;')}</pre>
       </div>`,
      `${n.hostname || n.id} · ${label}需要提权`,
      {
        dangerouslyUseHTMLString: true,
        confirmButtonText: '我已执行',
        cancelButtonText: '取消',
        showClose: false,
      },
    ).catch(() => null)
  } catch {
    /* 用户关闭对话框 */
  }
}

onMounted(load)
</script>

<template>
  <div style="display: flex; flex-direction: column; gap: 14px">
    <div style="display: flex; align-items: center; justify-content: space-between">
      <div class="text-secondary" style="font-size: 13px">
        双路组网：<b>Tailscale</b>（主通道，WireGuard mesh）＋ <b>FRP</b>（备用中继）。
        共 {{ nodes.length }} 个在线节点。
      </div>
      <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
    </div>

    <el-empty v-if="!loading && nodes.length === 0" description="当前没有在线节点" />

    <el-card v-for="n in nodes" :key="n.id" shadow="never">
      <template #header>
        <div style="display: flex; align-items: center; gap: 8px">
          <span class="status-dot is-online" />
          <b>{{ n.hostname || n.id }}</b>
          <span class="mono text-secondary" style="font-size: 12px">{{ n.id }}</span>
          <span v-if="n.tailscale_ip" class="mono text-secondary" style="font-size: 12px">{{ n.tailscale_ip }}</span>
          <span style="flex: 1"></span>
        </div>
      </template>

      <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 16px">
        <!-- Tailscale 卡片 -->
        <div class="card" style="margin: 0">
          <div class="card-header">
            <span>Tailscale</span>
            <el-tag size="small" :type="stateOf(n.id).tailscale.ok ? 'success' : 'info'">
              {{ stateOf(n.id).tailscale.ok ? '已连接' : '—' }}
            </el-tag>
          </div>
          <div class="card-body">
            <pre v-loading="!stateOf(n.id).tailscale.loaded" style="white-space: pre-wrap; font-size: 12px; line-height: 1.6; margin: 0; min-height: 40px">{{ stateOf(n.id).tailscale.summary }}</pre>
            <div style="display: flex; gap: 8px; margin-top: 10px">
              <el-button size="small" type="primary" :loading="busy === `${n.id}:tailscale:up`" @click="doAction(n, 'tailscale', 'up')">启用</el-button>
              <el-button size="small" type="warning" :loading="busy === `${n.id}:tailscale:down`" @click="doAction(n, 'tailscale', 'down')">停用</el-button>
            </div>
          </div>
        </div>

        <!-- FRP 卡片 -->
        <div class="card" style="margin: 0">
          <div class="card-header">
            <span>FRP (frpc)</span>
            <el-tag size="small" :type="stateOf(n.id).frp.running ? 'success' : stateOf(n.id).frp.configured ? 'warning' : 'info'">
              {{ stateOf(n.id).frp.running ? '运行中' : stateOf(n.id).frp.configured ? '未运行' : '未配置' }}
            </el-tag>
          </div>
          <div class="card-body">
            <div v-loading="!stateOf(n.id).frp.loaded" style="font-size: 12.5px; line-height: 1.7; min-height: 40px">{{ stateOf(n.id).frp.summary }}</div>
            <div style="display: flex; gap: 8px; margin-top: 10px">
              <el-button size="small" type="primary" :disabled="!stateOf(n.id).frp.configured" :loading="busy === `${n.id}:frp:up`" @click="doAction(n, 'frp', 'up')">启动</el-button>
              <el-button size="small" type="warning" :loading="busy === `${n.id}:frp:down`" @click="doAction(n, 'frp', 'down')">停止</el-button>
            </div>
          </div>
        </div>
      </div>
    </el-card>
  </div>
</template>
