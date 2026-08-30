/**
 * ECP 紧急通道 —— Cloudflare Worker 入口。
 *
 * 职责：
 *   - /agent?node_id=xxx   Agent 出站 WSS（校验 AGENT_TOKEN）
 *   - /gui?node_id=xxx     控制机 GUI 出站 WSS（校验 GUI_TOKEN）
 *   - /health              健康检查（纯 HTTP，无鉴权只回状态，不返回任何业务数据）
 *
 * 鉴权：Authorization: Bearer <token> 或 ?token=<token>（浏览器 WS 无法带 Header 时用）。
 * 通过后按 node_id 路由到对应 Durable Object 房间，连接建立即完成。
 *
 * 设计要点：
 *   - 两端全部主动出站连接，天然穿透 NAT，无需公网 IP/端口映射
 *   - 未授权请求一律拒绝（401），不返回任何节点信息
 *   - 流量为标准 TLS/WSS，域名由 Cloudflare 签发，合规可审计
 */

export interface Env {
  ROOMS: DurableObjectNamespace
  AGENT_TOKEN: string
  GUI_TOKEN: string
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url)
    const path = url.pathname

    if (path === '/health') {
      return json({ status: 'ok', service: 'ecp-relay' })
    }

    if (request.headers.get('Upgrade')?.toLowerCase() !== 'websocket') {
      return json({ status: 'error', message: '仅支持 WebSocket 升级请求' }, 426)
    }

    const nodeId = url.searchParams.get('node_id')
    if (!nodeId || !/^[A-Za-z0-9_-]{1,64}$/.test(nodeId)) {
      return json({ status: 'error', message: '缺少合法的 node_id' }, 400)
    }

    // 角色与令牌校验：/agent 用 AGENT_TOKEN，/gui 用 GUI_TOKEN
    let token = ''
    const authz = request.headers.get('Authorization') || ''
    if (authz.startsWith('Bearer ')) {
      token = authz.slice(7)
    } else {
      token = url.searchParams.get('token') || ''
    }

    let expected = ''
    let role = ''
    if (path === '/agent') {
      expected = env.AGENT_TOKEN || ''
      role = 'agent'
    } else if (path === '/gui') {
      expected = env.GUI_TOKEN || ''
      role = 'gui'
    } else {
      return json({ status: 'error', message: '未知路径' }, 404)
    }

    if (!expected) {
      return json({ status: 'error', message: '服务端未配置令牌' }, 500)
    }
    if (!constantTimeEqual(token, expected)) {
      return json({ status: 'error', message: '鉴权失败' }, 401)
    }

    // 路由到房间：message 处理在 Room 内完成
    const id = env.ROOMS.idFromName(nodeId)
    const room = env.ROOMS.get(id)
    return room.fetch(request, { headers: { 'X-Ecp-Role': role } })
  },
}

/** 常数时间字符串比较，防时序侧信道。 */
function constantTimeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false
  let diff = 0
  for (let i = 0; i < a.length; i++) {
    diff |= a.charCodeAt(i) ^ b.charCodeAt(i)
  }
  return diff === 0
}

function json(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
  })
}