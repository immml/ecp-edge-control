/**
 * 紧急通道（Cloudflare Worker 中转）客户端。
 *
 * 场景：Tailscale 直连不可达时，控制台经 Worker 的 /gui 房间操作节点。
 * 与 server API 关系：relay 是**降级通道**——主通道（server REST/gRPC）正常
 * 时不用它；主通道超时/节点离线时自动切换。
 *
 * 令牌：登录后从 server GET /api/v1/relay/config 获取（url + gui_token），
 * 不写死在前端。
 */
import { ResultStatus } from './client'

export interface RelayConfig {
  url: string      // wss://edge.api.immml.top
  gui_token: string
  enabled: boolean
}

interface RelayFrame {
  type: string
  seq?: number
  node_id?: string
  ts?: number
  cmd?: { type: string; params?: Record<string, unknown>; timeout_sec?: number }
  result?: {
    status: number
    message?: string
    stdout?: string
    stderr?: string
    exit_code?: number
    privilege_script?: string
    privilege_hint?: string
  }
}

class RelayClient {
  private ws: WebSocket | null = null
  private cfg: RelayConfig | null = null
  private seq = 0
  private pending = new Map<number, { resolve: (v: RelayFrame) => void; reject: (e: Error) => void; timer: ReturnType<typeof setTimeout> }>()
  private connecting = false
  private manualClose = false
  online = false

  setConfig(cfg: RelayConfig | null) {
    this.cfg = cfg
    if (!cfg?.enabled) {
      this.close()
      return
    }
    this.connect()
  }

  isEnabled(): boolean {
    return !!this.cfg?.enabled
  }

  isOnline(): boolean {
    return this.online
  }

  connect() {
    if (!this.cfg || this.connecting || this.ws) return
    this.connecting = true
    this.manualClose = false

    const url = this.cfg.url.replace(/\/$/, '')
    const ws = new WebSocket(`${url}/gui?node_id=console&token=${encodeURIComponent(this.cfg.gui_token)}`)
    this.ws = ws

    ws.onopen = () => {
      this.online = true
      this.connecting = false
      this.onStatusChange?.()
    }
    ws.onclose = () => {
      this.online = false
      this.connecting = false
      this.ws = null
      this.rejectAll(new Error('relay 通道断开'))
      this.onStatusChange?.()
      // 自动重连（指数退避，最长 30s）
      if (!this.manualClose) {
        setTimeout(() => this.connect(), 3000)
      }
    }
    ws.onerror = () => {
      // onclose 随后触发，统一处理
    }
    ws.onmessage = (ev) => {
      try {
        const frame = JSON.parse(String(ev.data)) as RelayFrame
        if (frame.type === 'result' && frame.seq !== undefined) {
          const p = this.pending.get(frame.seq)
          if (p) {
            clearTimeout(p.timer)
            this.pending.delete(frame.seq)
            p.resolve(frame)
          }
        } else if (frame.type === 'offline') {
          this.onNodeOffline?.(frame.node_id || '')
        }
      } catch {
        // 忽略无法解析的帧
      }
    }
  }

  close() {
    this.manualClose = true
    this.ws?.close()
    this.ws = null
    this.online = false
  }

  /**
   * 经 relay 下发指令并等待回执（Promise 化）。
   * 复用主通道的 commandType 语义：type 为字符串枚举（如 'SHELL'）。
   */
  execCommand(nodeId: string, type: string, params?: Record<string, unknown>, timeoutSec = 30): Promise<RelayFrame> {
    if (!this.ws || !this.online) {
      return Promise.reject(new Error('relay 通道未连接'))
    }
    const seq = ++this.seq
    const frame: RelayFrame = { type: 'command', seq, node_id: nodeId, cmd: { type, params, timeout_sec: timeoutSec } }
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(seq)
        reject(new Error('relay 指令超时'))
      }, (timeoutSec + 5) * 1000)
      this.pending.set(seq, { resolve, reject, timer })
      this.ws!.send(JSON.stringify(frame))
    })
  }

  private rejectAll(err: Error) {
    for (const [, p] of this.pending) {
      clearTimeout(p.timer)
      p.reject(err)
    }
    this.pending.clear()
  }

  onStatusChange?: () => void
  onNodeOffline?: (nodeId: string) => void
}

export const relay = new RelayClient()

/** base64 解码（复用 client.ts 行为） */
export function b64decode(s: string): string {
  try {
    const bin = atob(s)
    const bytes = Uint8Array.from(bin, (ch) => ch.charCodeAt(0))
    return new TextDecoder('utf-8').decode(bytes)
  } catch {
    return s // 已是明文则原样返回
  }
}

export { ResultStatus }