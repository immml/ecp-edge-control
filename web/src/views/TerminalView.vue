<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'

import { mockNodes } from '@/mock/nodes'

const route = useRoute()
const router = useRouter()

const nodeId = route.params.id as string
const node = mockNodes.find((n) => n.id === nodeId)

const hostEl = ref<HTMLDivElement>()
let term: Terminal | null = null
let fitAddon: FitAddon | null = null

// 后端就绪后这里换成真实的 WebSocket：/api/v1/nodes/{id}/terminal
// 目前用本地回显演示交互，保持与真实终端一致的观感。
function writeBanner(t: Terminal) {
  const host = node?.hostname ?? nodeId
  t.writeln('\x1b[1;34m●\x1b[0m 边缘节点控制台 · Web SSH')
  t.writeln(`\x1b[90m目标: ${host} (${node?.tailscaleIp ?? '-'})\x1b[0m`)
  t.writeln('')
  t.writeln('\x1b[33m提示：终端通道尚未接入后端（T2 进行中），当前为回显模式。\x1b[0m')
  t.writeln('')
  t.write('$ ')
}

function handleInput(t: Terminal, data: string) {
  // 回车
  if (data === '\r') {
    t.write('\r\n$ ')
    return
  }
  // 退格
  if (data === '\x7f') {
    t.write('\b \b')
    return
  }
  t.write(data)
}

function fit() {
  try {
    fitAddon?.fit()
  } catch {
    /* 容器尚未布局完成时忽略 */
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

  writeBanner(term)
  term.onData((data) => handleInput(term!, data))

  fit()
  window.addEventListener('resize', fit)
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', fit)
  term?.dispose()
  term = null
})
</script>

<template>
  <div style="height: 100%; display: flex; flex-direction: column; gap: 12px">
    <div style="display: flex; align-items: center; justify-content: space-between">
      <div style="display: flex; align-items: center; gap: 10px">
        <el-button size="small" text @click="router.push('/nodes')">← 返回</el-button>
        <span style="font-weight: 600">{{ node?.hostname ?? nodeId }}</span>
        <span class="chip mono">{{ node?.tailscaleIp }}</span>
      </div>
      <el-button size="small" @click="fit">适应窗口</el-button>
    </div>

    <div class="terminal-container" style="flex: 1; min-height: 0">
      <div ref="hostEl" style="height: 100%" />
    </div>
  </div>
</template>
