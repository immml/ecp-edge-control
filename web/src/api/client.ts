/**
 * 控制台后端 API 客户端（浏览器原生 fetch，无额外依赖）。
 *
 * 后端约定见架构 v2 §8.1：统一响应 { code, message, data }。
 * 未登录/凭据失效 → 401；本客户端自动清理本地 token 并跳登录页。
 */

const TOKEN_KEY = 'ecp_token'

export interface ApiNode {
  id: string
  hostname: string
  arch: string
  os: string
  agent_version: string
  status: string
  last_seen_at: string
  cpu_percent?: number
  mem_used_bytes?: number
  mem_total_bytes?: number
  containers_running?: number
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

  async audit(nodeId?: string): Promise<{ logs: any[]; total: number }> {
    const q = nodeId ? `?node_id=${encodeURIComponent(nodeId)}` : ''
    return this.request('GET', `api/v1/audit${q}`)
  }

  async execCommand(nodeId: string, type: string, params?: Record<string, unknown>): Promise<unknown> {
    return this.request('POST', `api/v1/nodes/${nodeId}/command`, { type, params })
  }

  logout() {
    this.setToken(null)
  }
}

export const api = new ApiClient()
