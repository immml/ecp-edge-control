/**
 * 控制台后端 API 客户端（浏览器原生 fetch，无额外依赖）。
 *
 * 后端约定见架构 v2 §8.1：统一响应 { code, message, data }。
 * 未登录/凭据失效 → 401；本客户端自动清理本地 token 并跳登录页。
 */

const TOKEN_KEY = 'ecp_token'

export interface RemoteFile {
  name: string
  path: string
  is_dir: boolean
  size: number
  mode: string
  modified_at: string
}

export interface Capabilities {
  canReadSystemStats: boolean
  canTerminal: boolean
  canManageFiles: boolean
  canReadDocker: boolean
  canWriteDocker: boolean
  canManageTailscale: boolean
  canManageNetwork: boolean
  canManageSystemd: boolean
  canSelfUpgrade: boolean
  canReadNetConfig: boolean
  runAsUid: number
  runAsUser: string
  missingTools: string[]
  panelEntrance?: string
}

export interface Telemetry {
  cpuPercent: number
  memUsedBytes: number
  memTotalBytes: number
  diskUsedBytes: number
  diskTotalBytes: number
  netRxBytes: number
  netTxBytes: number
  load1: number
  temperatureCelsius: number
  containersRunning: number
}

export interface ApiNode {
  id: string
  hostname: string
  arch: string
  os: string
  os_version?: string
  kernel?: string
  agent_version: string
  tailscale_ip?: string
  status: 'online' | 'offline' | 'unknown'
  last_seen_at: string
  registered_at?: string
  capabilities: Capabilities
  telemetry: Telemetry
}

export interface LoginResult {
  token: string
  username: string
  role: string
}

export interface TelemetrySample {
  id: number
  node_id: string
  ts: string
  cpu_percent: number
  mem_total_bytes: number
  mem_used_bytes: number
  disk_total_bytes: number
  disk_used_bytes: number
  net_rx_bytes: number
  net_tx_bytes: number
  load1: number
  temperature_celsius: number
  containers_running: number
}

export interface TelemetryResult {
  items: TelemetrySample[] // 最新在前
  latest: TelemetrySample | null
}

export interface NodeDetail {
  node: ApiNode
  online: boolean
  telemetry: TelemetrySample | null
}

export interface BatchResult {
  node_id: string
  status: 'ok' | 'failed' | 'offline' | 'rejected'
  message: string
  stdout?: string
}

export interface ContainerInfo {
  name: string
  image: string
  status: string
  state: string
  ports: string
  labels: string
  managed: string
}

// 与 proto ecp.v1.ResultStatus 对应（server 用 JSON 序列化 enum 输出数字）
export const ResultStatus = {
  OK: 1,
  FAILED: 2,
  NEEDS_PRIVILEGE: 3,
  TIMEOUT: 4,
  REJECTED: 5,
} as const

export interface CommandResult {
  status: number
  message: string
  stdout: string
  privilege_hint?: string
  privilege_script?: string
}

class ApiClient {
  private base = '/'

  get token(): string | null {
    return localStorage.getItem(TOKEN_KEY)
  }

  setToken(t: string | null) {
    if (t) localStorage.setItem(TOKEN_KEY, t)
    else localStorage.removeItem(TOKEN_KEY)
  }

  isAuthed(): boolean {
    return !!this.token
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' }
    const tk = this.token
    if (tk) headers['Authorization'] = `Bearer ${tk}`

    const res = await fetch(this.base + path, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    })

    if (res.status === 401) {
      this.setToken(null)
      if (location.pathname !== '/login') location.href = '/login'
      throw new Error('未认证或登录已失效')
    }
    const json = (await res.json()) as { code: number; message: string; data?: T }
    if (json.code !== 0) {
      throw new Error(json.message || '请求失败')
    }
    return json.data as T
  }

  async login(username: string, password: string): Promise<LoginResult> {
    const data = await this.request<LoginResult>('POST', 'api/v1/login', { username, password })
    this.setToken(data.token)
    return data
  }

  me(): Promise<{ username: string; role: string }> {
    return this.request('GET', 'api/v1/me')
  }

  /** 修改当前用户密码（校验旧密码）。 */
  async changePassword(oldPassword: string, newPassword: string): Promise<void> {
    await this.request('POST', 'api/v1/change-password', { old_password: oldPassword, new_password: newPassword })
  }

  async listNodes(): Promise<ApiNode[]> {
    const r = await this.request<{ nodes: ApiNode[]; total: number }>('GET', 'api/v1/nodes')
    return r.nodes
  }

  async getNode(id: string): Promise<NodeDetail> {
    return this.request('GET', `api/v1/nodes/${id}`)
  }

  async getTelemetry(nodeId: string, limit = 120): Promise<TelemetryResult> {
    return this.request('GET', `api/v1/nodes/${nodeId}/telemetry?limit=${limit}`)
  }

  async listFiles(nodeId: string, path: string): Promise<{ path: string; items: RemoteFile[] }> {
    return this.request('GET', `api/v1/nodes/${nodeId}/files?path=${encodeURIComponent(path)}`)
  }

  async audit(nodeId?: string): Promise<{ logs: any[]; total: number }> {
    const q = nodeId ? `?node_id=${encodeURIComponent(nodeId)}` : ''
    return this.request('GET', `api/v1/audit${q}`)
  }

  async execCommand(nodeId: string, type: string, params?: Record<string, unknown>, timeoutSec = 0): Promise<CommandResult> {
    const r = await this.request<CommandResult>('POST', `api/v1/nodes/${nodeId}/command`, { type, params, timeout_sec: timeoutSec || undefined })
    // status 归一化为数字（兼容未来 protojson 字符串形态）
    r.status = Number(r.status) || 0
    // protojson 把 bytes 字段（stdout）序列化为 base64，这里统一还原为 UTF-8 文本
    if (r && typeof r.stdout === 'string' && r.stdout) {
      try {
        const bin = atob(r.stdout)
        const bytes = Uint8Array.from(bin, (ch) => ch.charCodeAt(0))
        r.stdout = new TextDecoder('utf-8').decode(bytes)
      } catch {
        // 已是明文（如 server 侧直接 string 化的场景），保留原值
      }
    }
    return r
  }

  /**
   * 紧急通道配置：登录后调用，返回 relay 连接参数（url + gui_token）。
   * server 未启用 relay 时返回 enabled=false，前端不做任何连接。
   */
  async getRelayConfig(): Promise<{ enabled: boolean; url: string; gui_token: string }> {
    return this.request('GET', 'api/v1/relay/config')
  }

  /** 经紧急通道（relay）执行指令：主通道失败时自动降级。 */
  async execCommandRelay(nodeId: string, type: string, params?: Record<string, unknown>, timeoutSec = 0): Promise<CommandResult> {
    const { relay, b64decode } = await import('./relay')
    if (!relay.isEnabled() || !relay.isOnline()) {
      throw new Error('紧急通道不可用')
    }
    const frame = await relay.execCommand(nodeId, type, params, timeoutSec)
    const res = frame.result || { status: 0, message: '无回执' }
    return {
      status: Number(res.status) || 0,
      message: res.message || '',
      stdout: res.stdout ? b64decode(res.stdout) : '',
      stderr: res.stderr ? b64decode(res.stderr) : '',
      exit_code: res.exit_code ?? 0,
      privilege_script: res.privilege_script || '',
      privilege_hint: res.privilege_hint || '',
    } as unknown as CommandResult
  }

  /** 统一入口：优先主通道，失败自动降级紧急通道（仅当 relay 已启用）。 */
  async execCommandAuto(nodeId: string, type: string, params?: Record<string, unknown>, timeoutSec = 0): Promise<CommandResult> {
    try {
      return await this.execCommand(nodeId, type, params, timeoutSec)
    } catch (e) {
      const { relay } = await import('./relay')
      if (relay.isEnabled() && relay.isOnline()) {
        return this.execCommandRelay(nodeId, type, params, timeoutSec)
      }
      throw e
    }
  }

  async batchCommand(nodeIds: string[], type: string, params?: Record<string, unknown>): Promise<{ total: number; results: BatchResult[] }> {
    return this.request('POST', 'api/v1/nodes/batch/command', { node_ids: nodeIds, type, params })
  }

  // —— VPN / Clash ——
  /** 导出 Clash 配置（访问内网设备）；返回 yaml 文本。 */
  async exportClash(cfg: { name?: string; server: string; port?: number; uuid: string; path?: string; extra_ips?: string }): Promise<string> {
    const resp = await fetch(this.base + 'api/v1/vpn/clash-config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${this.token}` },
      body: JSON.stringify(cfg),
    })
    if (!resp.ok) {
      const j = await resp.json().catch(() => null)
      throw new Error((j && j.message) || `HTTP ${resp.status}`)
    }
    return resp.text()
  }

  // —— OTA ——
  async uploadAgent(file: File): Promise<{ name: string; sha256: string; size: number }> {
    const fd = new FormData()
    fd.append('file', file)
    const resp = await fetch(this.base + 'api/v1/agent/upload', {
      method: 'POST',
      headers: { Authorization: `Bearer ${this.token}` },
      body: fd,
    })
    const data = await resp.json()
    if (!resp.ok || data.code !== 0) throw new Error(data.message || '上传失败')
    return data.data
  }

  async upgradeNode(nodeId: string, binary: string): Promise<{ upgrading: boolean; needs_privilege?: boolean; privilege_script?: string }> {
    return this.request('POST', `api/v1/nodes/${nodeId}/upgrade`, { binary })
  }

  // —— 告警 ——
  async listAlertRules(): Promise<{ rules: any[]; total: number }> {
    return this.request('GET', 'api/v1/alerts/rules')
  }
  async createAlertRule(body: Record<string, unknown>): Promise<any> {
    return this.request('POST', 'api/v1/alerts/rules', body)
  }
  async updateAlertRule(id: number, body: Record<string, unknown>): Promise<any> {
    return this.request('PUT', `api/v1/alerts/rules/${id}`, body)
  }
  async deleteAlertRule(id: number): Promise<any> {
    return this.request('DELETE', `api/v1/alerts/rules/${id}`)
  }
  async deployAlertRule(nodeId: string): Promise<{ deployed: boolean; count: number }> {
    return this.request('POST', 'api/v1/alerts/rules/deploy', { node_id: nodeId })
  }
  async listAlertEvents(limit = 200): Promise<{ events: any[]; total: number }> {
    return this.request('GET', `api/v1/alerts/events?limit=${limit}`)
  }
  async markAlertEventsRead(): Promise<any> {
    return this.request('POST', 'api/v1/alerts/events/read')
  }

  logout() {
    this.setToken(null)
  }
}

export const api = new ApiClient()
