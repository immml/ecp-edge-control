// ECP relay 端到端 WS 测试 v3：打印详细错误
const base = 'wss://ecp-relay.llqi-yx-1.workers.dev'
const AGENT_TOKEN = process.env.AGENT_TOKEN
const GUI_TOKEN = process.env.GUI_TOKEN

const results = []

function connect(path, token, label, timeoutMs = 10000) {
  return new Promise((resolve) => {
    const ws = new WebSocket(`${base}${path}?node_id=n-e2e&token=${encodeURIComponent(token)}`)
    const timer = setTimeout(() => { try { ws.close() } catch {} ; resolve(`${label}: TIMEOUT`) }, timeoutMs)
    ws.onopen = () => { clearTimeout(timer); resolve(`${label}: OPEN`) }
    ws.onerror = (e) => { clearTimeout(timer); resolve(`${label}: ERROR ${e?.message || ''}`) }
    ws.onclose = (e) => { clearTimeout(timer); resolve(`${label}: CLOSE code=${e.code} reason=${e.reason} clean=${e.wasClean}`) }
  })
}

results.push(await connect('/agent', 'wrongtoken', 'E1 错token'))
results.push(await connect('/agent', AGENT_TOKEN, 'E2 Agent对token'))
results.push(await connect('/gui', GUI_TOKEN, 'E3 GUI对token'))
results.push(await connect('/gui', AGENT_TOKEN, 'E4 跨角色token'))

console.log(results.join('\n'))