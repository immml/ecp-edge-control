/**
 * ECP 紧急通道 —— Durable Object 房间。
 *
 * 每个 node_id 一个房间（DO idFromName(nodeId)）。房间内：
 *   - agentWs：Agent 连接（每节点唯一，新连接顶掉旧连接）
 *   - guiWs：  控制机 GUI 连接（可有多个，如多浏览器/多设备同时看）
 *
 * 转发规则：
 *   agent → gui：广播给所有在线 GUI（遥测、结果、离线事件）
 *   gui   → agent：转给 Agent；Agent 不在线则落 SQLite 暂存，上线后按 seq 补发
 *
 * 心跳：Agent 每 30s 发 ping；本对象记录 lastAgentPing，60s 无心跳判离线，
 * 广播 offline 事件给 GUI。离线指令存 SQLite（Free 计划 5GB 存储上限，仅存少量指令）。
 */

export interface Env {
  ROOMS: DurableObjectNamespace
  AGENT_TOKEN: string
  GUI_TOKEN: string
}

interface Frame {
  type: string
  seq?: number
  node_id?: string
  ts?: number
  [key: string]: unknown
}

const AGENT_OFFLINE_MS = 60_000
const HEARTBEAT_CHECK_MS = 15_000

export class Room {
  private conns: WebSocket[] = []
  private agentWs: WebSocket | null = null
  private guiWs: Set<WebSocket> = new Set()
  private lastAgentPing = 0
  private alarmScheduled = false

  constructor(private state: DurableObjectState, private env: Env) {
    this.state.blockConcurrencyWhile(async () => {
      await this.state.storage.sql.exec(`CREATE TABLE IF NOT EXISTS offline (
        seq INTEGER PRIMARY KEY,
        ts INTEGER NOT NULL,
        payload TEXT NOT NULL
      )`)
    })
  }

  async fetch(request: Request): Promise<Response> {
    const role = request.headers.get('X-Ecp-Role') || ''

    if (request.headers.get('Upgrade')?.toLowerCase() !== 'websocket') {
      return new Response(JSON.stringify({ status: 'ok', service: 'ecp-relay-room' }), {
        headers: { 'Content-Type': 'application/json' },
      })
    }

    const pair = new WebSocketPair()
    const ws = pair[1]
    this.state.acceptWebSocket(ws)

    if (role === 'agent') {
      this.attachAgent(ws)
    } else {
      this.attachGui(ws)
    }

    return new Response(null, { status: 101, webSocket: pair[0] })
  }

  // ---- 连接管理 ----

  private attachAgent(ws: WebSocket): void {
    // 新 Agent 顶掉旧连接（重连场景）
    if (this.agentWs && this.agentWs !== ws) {
      try {
        this.agentWs.close(4001, 'replaced by new agent connection')
      } catch {
        /* ignore */
      }
    }
    this.agentWs = ws
    this.conns.push(ws)
    this.lastAgentPing = Date.now()
    this.armHeartbeatAlarm()
    console.log('[room] agent attached', { room: this.name })

    // 补发离线期间暂存的指令
    this.flushOffline(ws)
  }

  private get name(): string {
    // DO 实例名（node_id）；仅用于日志
    return this.state.id.toString()
  }

  private attachGui(ws: WebSocket): void {
    this.guiWs.add(ws)
    this.conns.push(ws)
    console.log('[room] gui attached', { room: this.name, guis: this.guiWs.size })
  }

  // ---- 消息处理（acceptWebSocket 后由运行时驱动事件回调） ----

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    if (typeof message !== 'string') {
      // Agent 上行帧统一为 JSON；二进制忽略（避免意外解析失败炸掉房间）
      return
    }

    let frame: Frame
    try {
      frame = JSON.parse(message) as Frame
    } catch {
      return
    }

    const isFromAgent = ws === this.agentWs
    console.log('[room] msg', { type: frame.type, fromAgent: isFromAgent, room: this.name })

    switch (frame.type) {
      case 'ping':
        if (isFromAgent) {
          this.lastAgentPing = Date.now()
          this.safeSend(ws, JSON.stringify({ type: 'pong', ts: Date.now() }))
        }
        break

      case 'telemetry':
        // 心跳与遥测合一：Agent 的任何上行都刷新 lastAgentPing
        if (isFromAgent) {
          this.lastAgentPing = Date.now()
          this.broadcastGui(frame)
        }
        break

      case 'result':
        if (isFromAgent) {
          this.lastAgentPing = Date.now()
          this.broadcastGui(frame)
        }
        break

      case 'command':
        // GUI → Agent；Agent 离线则暂存
        if (!isFromAgent) {
          if (this.agentWs) {
            this.safeSend(this.agentWs, message)
          } else {
            await this.queueOffline(frame)
          }
        }
        break

      default:
        // 未知帧：从非 agent 发来的转发到 agent 也无意义，忽略
        break
    }
  }

  webSocketClose(ws: WebSocket): void {
    this.detach(ws)
  }

  webSocketError(ws: WebSocket): void {
    this.detach(ws)
  }

  private detach(ws: WebSocket): void {
    const i = this.conns.indexOf(ws)
    if (i >= 0) this.conns.splice(i, 1)

    if (ws === this.agentWs) {
      this.agentWs = null
      // Agent 掉线：通知所有 GUI
      this.broadcastGui({ type: 'offline', node_id: String(ws.url || ''), ts: Date.now() })
    }
    this.guiWs.delete(ws)
  }

  // ---- 转发辅助 ----

  private broadcastGui(frame: Frame): void {
    const raw = JSON.stringify(frame)
    for (const g of this.guiWs) {
      this.safeSend(g, raw)
    }
  }

  private safeSend(ws: WebSocket, raw: string): void {
    try {
      ws.send(raw)
    } catch {
      /* 连接已死，由 close/error 事件清理 */
    }
  }

  // ---- 离线暂存与补发 ----

  private async queueOffline(frame: Frame): Promise<void> {
    const seq = typeof frame.seq === 'number' ? frame.seq : 0
    if (seq <= 0) {
      return // 无 seq 的帧不暂存，避免乱序补发
    }
    try {
      await this.state.storage.sql.exec(
        `INSERT OR REPLACE INTO offline (seq, ts, payload) VALUES (?, ?, ?)`,
        seq,
        Date.now(),
        JSON.stringify(frame),
      )
    } catch {
      /* 存储异常不阻断在线转发 */
    }
  }

  private async flushOffline(ws: WebSocket): Promise<void> {
    try {
      const rows = await this.state.storage.sql.exec(`SELECT seq, payload FROM offline ORDER BY seq LIMIT 100`)
      for (const row of rows.toArray()) {
        const payload = row.payload as string
        this.safeSend(ws, payload)
        const seq = row.seq as number
        await this.state.storage.sql.exec(`DELETE FROM offline WHERE seq = ?`, seq)
      }
    } catch {
      /* 补发失败：下次重连再试 */
    }
  }

  // ---- 心跳判活（alarm 驱动，不占连接消息额度） ----

  private armHeartbeatAlarm(): void {
    if (this.alarmScheduled) return
    this.alarmScheduled = true
    this.state.storage.setAlarm(Date.now() + HEARTBEAT_CHECK_MS)
  }

  async alarm(): Promise<void> {
    this.alarmScheduled = false

    const now = Date.now()
    if (this.agentWs && now - this.lastAgentPing > AGENT_OFFLINE_MS) {
      // 心跳超时 → 判离线，断开连接并通知 GUI
      try {
        this.agentWs.close(4000, 'heartbeat timeout')
      } catch {
        /* ignore */
      }
      this.detach(this.agentWs)
    } else if (this.agentWs) {
      // Agent 仍在线：下个周期再查
      this.armHeartbeatAlarm()
    }
  }
}