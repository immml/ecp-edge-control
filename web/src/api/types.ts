/**
 * 控制台与后端之间的数据类型定义。
 *
 * 字段来源：proto/ecp/v1/ecp.proto 与 server/internal/store/model。
 * 目前页面消费 mock 数据，后端就绪后只替换数据源，组件不改。
 */

/** 节点在线状态 */
export type NodeStatus = 'online' | 'offline' | 'unknown'

/** RBAC 三级角色 */
export type Role = 'admin' | 'operator' | 'viewer'

/** Agent 能力探测结果。与 proto 的 CapabilityReport 对应。 */
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
}

/** 遥测采样。与 proto 的 Telemetry 对应。 */
export interface Telemetry {
  cpuPercent: number
  memTotalBytes: number
  memUsedBytes: number
  diskTotalBytes: number
  diskUsedBytes: number
  netRxBytes: number
  netTxBytes: number
  load1: number
  load5: number
  temperatureCelsius: number
  containerRunning: number
  containerTotal: number
}

/** 节点 */
export interface Node {
  id: string
  hostname: string
  arch: string
  os: string
  osVersion: string
  kernel: string
  agentVersion: string
  tailscaleIp: string
  status: NodeStatus
  capabilities: Capabilities
  telemetry: Telemetry
  registeredAt: string
  lastSeenAt: string
}

/** 容器。managed 为 false 的是节点上既有的业务容器，ECP 不对其执行写操作。 */
export interface Container {
  id: string
  name: string
  image: string
  state: 'running' | 'exited' | 'paused'
  status: string
  /** 是否由 ECP 纳管（带 ecp.managed 标签） */
  managed: boolean
}

/** 审计日志条目 */
export interface AuditLog {
  id: number
  ts: string
  username: string
  nodeId: string
  action: string
  params: string
  result: string
  detail: string
  traceId: string
}

/** 文件项 */
export interface FileEntry {
  name: string
  path: string
  isDir: boolean
  size: number
  mode: string
  modifiedAt: string
}

/** 1Panel 隧道票据 */
export interface PanelTunnel {
  url: string
  expiresAt: string
}

/** 统一的 API 响应包装 */
export interface ApiResponse<T> {
  code: number
  message: string
  data: T
}
