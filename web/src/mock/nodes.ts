/**
 * mock 数据。
 *
 * 数值全部取自真机实测（Orange Pi 3B，见 docs/真机环境报告.md），
 * 不编造——这样界面上的数字在真机联调时不会让人产生错误的量级预期。
 */

import type { AuditLog, Container, Node, Telemetry } from '@/api/types'

/** 真机能力探测的实际结果（2026-08-29 在 100.108.234.5 上跑 ecp-agent caps 的输出） */
const realCapabilities = {
  canReadSystemStats: true,
  canTerminal: true,
  canManageFiles: true,
  canReadDocker: true,
  canWriteDocker: true,
  canManageTailscale: true,
  // 真机未安装 iptables（Debian 12 转向 nftables），故为 false
  canManageNetwork: false,
  canManageSystemd: true,
  canSelfUpgrade: true,
  canReadNetConfig: true,
  runAsUid: 1000,
  runAsUser: 'orangepi',
  missingTools: ['iptables'],
}

const realTelemetry: Telemetry = {
  cpuPercent: 37.4,
  // 真机内存 3.8 GiB
  memTotalBytes: 3.8 * 1024 ** 3,
  memUsedBytes: 1.2 * 1024 ** 3,
  // 真机根分区 232G NVMe，已用 25G
  diskTotalBytes: 232 * 1024 ** 3,
  diskUsedBytes: 25 * 1024 ** 3,
  netRxBytes: 84_213_665_792,
  netTxBytes: 21_884_301_312,
  // 真机负载 8.11（四核）
  load1: 8.11,
  load5: 6.66,
  // 真机温度 56.1°C
  temperatureCelsius: 56.1,
  containerRunning: 1,
  containerTotal: 3,
}

export const mockNodes: Node[] = [
  {
    id: 'nd-orangepi3b',
    hostname: 'orangepi3b',
    arch: 'arm64',
    os: 'linux',
    osVersion: 'debian 12 (bookworm)',
    kernel: '5.10.160-rockchip-rk356x',
    agentVersion: 'dev',
    tailscaleIp: '100.108.234.5',
    status: 'online',
    capabilities: realCapabilities,
    telemetry: realTelemetry,
    registeredAt: '2026-08-29T16:44:00+08:00',
    lastSeenAt: new Date().toISOString(),
  },
  {
    id: 'nd-zero',
    hostname: 'zero',
    arch: 'amd64',
    os: 'windows',
    osVersion: 'windows',
    kernel: '-',
    agentVersion: 'dev',
    tailscaleIp: '100.68.202.101',
    status: 'online',
    capabilities: { ...realCapabilities, runAsUser: 'flowe', canReadDocker: false, canWriteDocker: false },
    telemetry: { ...realTelemetry, cpuPercent: 12.6, memUsedBytes: 9.4 * 1024 ** 3, memTotalBytes: 32 * 1024 ** 3 },
    registeredAt: '2026-08-29T17:05:00+08:00',
    lastSeenAt: new Date().toISOString(),
  },
  {
    id: 'nd-offline-demo',
    hostname: 'edge-box-07',
    arch: 'arm64',
    os: 'linux',
    osVersion: 'debian 12 (bookworm)',
    kernel: '5.10.160-rockchip-rk356x',
    agentVersion: 'dev',
    tailscaleIp: '100.91.12.34',
    status: 'offline',
    capabilities: { ...realCapabilities, canReadDocker: false, canWriteDocker: false },
    telemetry: { ...realTelemetry, cpuPercent: 0, memUsedBytes: 0, load1: 0 },
    registeredAt: '2026-08-20T09:12:00+08:00',
    lastSeenAt: '2026-08-28T23:41:00+08:00',
  },
]

/** 真机 docker ps 的实际结果：wxedge 是业务容器，非 ECP 纳管 */
export const mockContainers: Container[] = [
  {
    id: 'c-wxedge',
    name: 'wxedge',
    image: 'onething1/wxedge:latest',
    state: 'running',
    status: 'Up 2 hours',
    managed: false,
  },
  {
    id: 'c-nginx-old',
    name: 'nginx-old',
    image: 'nginx:1.24',
    state: 'exited',
    status: 'Exited (0) 3 days ago',
    managed: false,
  },
  {
    id: 'c-redis-old',
    name: 'redis-cache',
    image: 'redis:7-alpine',
    state: 'exited',
    status: 'Exited (0) 5 days ago',
    managed: false,
  },
]

export const mockAuditLogs: AuditLog[] = [
  {
    id: 3,
    ts: '2026-08-29T17:35:12+08:00',
    username: 'immml',
    nodeId: 'nd-orangepi3b',
    action: 'agent.caps.probe',
    params: '{}',
    result: 'ok',
    detail: 'docker=true tailscale=true iptables=false',
    traceId: 'tr-9f2c1a',
  },
  {
    id: 2,
    ts: '2026-08-29T17:15:44+08:00',
    username: 'immml',
    nodeId: 'nd-orangepi3b',
    action: 'node.docker.group.grant',
    params: '{"user":"orangepi","group":"docker"}',
    result: 'ok',
    detail: '老板决策：接受 docker 组 ≈ root 等价权限的风险',
    traceId: 'tr-7b31e0',
  },
  {
    id: 1,
    ts: '2026-08-29T16:59:22+08:00',
    username: 'immml',
    nodeId: 'nd-orangepi3b',
    action: 'node.probe',
    params: '{}',
    result: 'ok',
    detail: '首次环境探测，生成 docs/真机环境报告.md',
    traceId: 'tr-1a0847',
  },
]
