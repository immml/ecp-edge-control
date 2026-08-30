/**
 * ECP 紧急通道 —— Durable Object 房间。
 *
 * 每个 node_id 一个房间（DO idFromName(nodeId)）。房间内：
 *   - Agent 连接（每节点唯一，tag='agent'，新连接顶掉旧连接）
 *   - GUI 连接（可多个，tag='gui'，如多浏览器/多设备同时看）
 *
 * 转发规则：
 *   agent → gui：广播给所有在线 GUI（遥测、结果、离线事件）
 *   gui   → agent：转给 Agent；Agent 不在线则落 SQLite 暂存，上线后按 seq 补发
 *
 * 心跳：Agent 每 30s 发 ping；60s 无心跳判离线，广播 offline 事件给 GUI。
 *
 * 关键设计：**使用 WebSocket Hibernation API（acceptWebSocket with tags）**。
 * DO 空闲冻结时内存字段会全部清空，但连接与 tag 由运行时保留——所以
 * 转发/判活全部依赖 `state.getWebSockets(tag)` 而非内存字段，冻结唤醒后
 * 身份判定依然正确。这是本房间避免"内存态丢失导致链路静默中断"的根本。
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

const TAG_AGENT = 'agent'
const TAG_GUI = 'gui'
const AGENT_OFFLINE_MS = 60_000
const HEARTBEAT_CHECK_MS = 15_000

export class Room {
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
    // 角色判定：以 URL path 为准（/agent 或 /gui），不依赖自定义 header——
    // header 在 WebSocket 升级跨 Worker→DO 边界时可能丢失，path 稳定可靠。
    const url = new URL(request.url)
    const role = url.pathname === '/agent' ? TAG_AGENT : TAG_GUI

    if (request.headers.get('Upgrade')?.toLowerCase() !== 'websocket') {
      return new Response(JSON.stringify({ status: 'ok', service: 'ecp-relay-room' }), {
        headers: { 'Content-Type': 'application/json' },
      })
    }

    const pair = new WebSocketPair()
    const ws = pair[1]
    // Hibernation 风格 accept：tag 进 storage，冻结唤醒后仍可 getWebSockets(tag) 找回
    this.state.acceptWebSocket(ws, [role])

    if (role === TAG_AGENT) {
      this.attachAgent(ws)
    } else {
      this.attachGui(ws)
    }

    return new Response(null, { status: 101, webSocket: pair[0] })
  }

  // ---- 连接管理（态全部来自 storage，不依赖内存）----

  private attachAgent(ws: WebSocket): void {
    // 新 Agent 顶掉旧连接（重连场景）；旧连接从 storage 里找
    for (const old of this.agentConns()) {
      if (old !== ws) {
        try {
          old.close(4001, 'replaced by new agent connection')
        } catch {
          /* ignore */
        }
      }
    }
    this.lastAgentPing = Date.now()
    this.armHeartbeatAlarm()
    console.log('[room] agent attached', { room: this.name })

    // 补发离线期间暂存的指令
    this.flushOffline(ws)
  }

  private attachGui(ws: WebSocket): void {
    console.log('[room] gui attached', { room: this.name, guis: this.guiConns().length })
  }

  private agentConns(): WebSocket[] {
    return this.state.getWebSockets(TAG_AGENT)
  }

  private guiConns(): WebSocket[] {
    return this.state.getWebSockets(TAG_GUI)
  }

  private get name(): string {
    return this.state.id.toString()
  }

  // ---- 消息处理（Hibernation 由运行时驱动，冻结唤醒后自动恢复）----

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    if (typeof message !== 'string') {
      return
    }

    let frame: Frame
    try {
      frame = JSON.parse(message) as Frame
    } catch {
      return
    }

    const isFromAgent = this.agentConns().includes(ws)

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
        // GUI → Agent；Agent 不在线则暂存（等待上线补发）
        if (!isFromAgent) {
          const agent = this.agentConns()[0]
          if (agent) {
            this.safeSend(agent, message)
          } else {
            await this.queueOffline(frame)
          }
        }
        break

      default:
        break
    }
  }

  webSocketClose(ws: WebSocket): void {
    // Hibernation：关闭的连接自动从 tag 集合移除，无需手动 detach。
    // 但若是 agent 连接断开，需广播 offline 给 GUI。
    if (this.agentConns().includes(ws)) {
      this.broadcastGui({ type: 'offline', node_id: this.name, ts: Date.now() })
    }
  }

  webSocketError(ws: WebSocket): void {
    this.webSocketClose(ws)
  }

  // ---- 转发辅助 ----

  private broadcastGui(frame: Frame): void {
    const raw = JSON.stringify(frame)
    for (const g of this.guiConns()) {
      this.safeSend(g, raw)
    }
  }

  private safeSend(ws: WebSocket, raw: string): void {
    try {
      ws.send(raw)
    } catch {
      /* 连接已死，由 close 事件清理 */
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
        this.safeSend(ws, row.payload as string)
        await this.state.storage.sql.exec(`DELETE FROM offline WHERE seq = ?`, row.seq as number)
      }
    } catch {
      /* 补发失败：下次重连再试 */
    }
  }

  // ---- 心跳判活（alarm 驱动，不占连接消息额度）----

  private armHeartbeatAlarm(): void {
    if (this.alarmScheduled) return
    this.alarmScheduled = true
    this.state.storage.setAlarm(Date.now() + HEARTBEAT_CHECK_MS)
  }

  async alarm(): Promise<void> {
    this.alarmScheduled = false

    const now = Date.now()
    const agent = this.agentConns()[0]
    if (agent && now - this.lastAgentPing > AGENT_OFFLINE_MS) {
      // 心跳超时 → 判离线，断开连接并通知 GUI
      try {
        agent.close(4000, 'heartbeat timeout')
      } catch {
        /* ignore */
      }
      this.broadcastGui({ type: 'offline', node_id: this.name, ts: Date.now() })
    } else if (agent) {
      // Agent 仍在线：下个周期再查
      this.armHeartbeatAlarm()
    }
  }
}