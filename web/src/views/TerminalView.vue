<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'

import { api } from '@/api/client'

const route = useRoute()
const router = useRouter()

const nodeId = route.params.id as string
const hostEl = ref<HTMLDivElement>()
let term: Terminal | null = null
let fitAddon: FitAddon | null = null
let ws: WebSocket | null = null
let reconnectTimer = 0
let closed = false

function fit() {
  try {
    fitAddon?.fit()
  } catch {
    /* 容器尚未布局完成时忽略 */
  }
}

function connect() {
  if (closed || !api.token) return
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const cols = Math.max(term?.cols ?? 80, 10)
  const rows = Math.max(term?.rows ?? 24, 5)
  const url = `${proto}://${location.host}/api/v1/nodes/${nodeId}/terminal/ws?token=${encodeURIComponent(api.token)}&cols=${cols}&rows=${rows}`
  ws = new WebSocket(url)

  ws.onopen = () => {
    term?.writeln('\x1b[90m[已连接节点终端]\x1b[0m')
  }
  ws.onmessage = (ev) => {
    term?.write(typeof ev.data === 'string' ? ev.data : '')
  }
  ws.onclose = () => {
    if (closed) return
    term?.writeln('\r\n\x1b[33m[连接断开，3 秒后重连…]\x1b[0m')
    reconnectTimer = window.setTimeout(connect, 3000)
  }
  ws.onerror = () => {
    ws?.close()
  }
}

onMounted(() => {
  if (!hostEl.value) return

  term = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: "'JetBrains Mono', 'SF Mono', Consolas, monospace",
    theme: {
      background: '#0d1117',
      foreground: '#c9d1d9',
      cursor: '#6b37c9',
    },
    allowProposedApi: true,
  })
  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(hostEl.value)

  term.onData((data) => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(data)
    }
  })

  fit()
  window.addEventListener('resize', fit)
  connect()
})

onBeforeUnmount(() => {
  closed = true
  window.clearTimeout(reconnectTimer)
  window.removeEventListener('resize', fit)
  ws?.close()
  ws = null
  term?.dispose()
  term = null
})
</script>

<template>
  <div style="height: 100%; display: flex; flex-direction: column; gap: 12px">
    <div style="display: flex; align-items: center; justify-content: space-between">
      <div style="display: flex; align-items: center; gap: 10px">
        <el-button size="small" text @click="router.push('/nodes')">← 返回</el-button>
        <span style="font-weight: 600">{{ nodeId }}</span>
        <span class="chip">Web SSH</span>
      </div>
      <el-button size="small" @click="fit">适应窗口</el-button>
    </div>

    <div class="terminal-container" style="flex: 1; min-height: 0">
      <div ref="hostEl" style="height: 100%" />
    </div>
  </div>
</template>
