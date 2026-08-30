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
      // 节点令牌 = HMAC-SHA256(AGENT_TOKEN 主密钥, node_id)（十六进制小写）。
      // 每节点独立令牌：泄露一个节点令牌不影响其他节点；主密钥仍在 Secrets 中加密存放。
      // 兼容：主密钥原值也接受（旧版 Agent 直配主密钥时不中断）。
      const master = env.AGENT_TOKEN || ''
      expected = master ? await hmacSha256Hex(master, nodeId) : ''
      if (expected && constantTimeEqual(token, master)) {
        expected = master // 兼容旧配置直配 AGENT_TOKEN
      }
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

    // 路由到房间：克隆请求并追加角色头，保留原始 Upgrade/Connection 等头
    const id = env.ROOMS.idFromName(nodeId)
    const room = env.ROOMS.get(id)
    const upstream = new Request(request, {
      headers: new Headers([
        ...request.headers.entries(),
        ['X-Ecp-Role', role],
      ]),
    })
    return room.fetch(upstream)
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

/** hmacSha256Hex 计算 HMAC-SHA256(key, msg) 的十六进制串（Web Crypto 异步）。 */
async function hmacSha256Hex(key: string, msg: string): Promise<string> {
  const enc = new TextEncoder()
  const keyBuf = await crypto.subtle.importKey(
    'raw',
    enc.encode(key),
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign'],
  )
  const sig = await crypto.subtle.sign('HMAC', keyBuf, enc.encode(msg))
  return Array.from(new Uint8Array(sig))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
}

function json(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
  })
}

// Durable Object 必须在入口文件导出，wrangler 才能识别（否则打包时找不到类）
export { Room } from './room'