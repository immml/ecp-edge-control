# 紧急通道设计：HTTPS/WSS 适配 Cloudflare Worker 中转

> 日期：2026-08-30
> 范围：三通路系统中的**通路②（紧急/临时控制）**——客户机无法 Tailscale 直连时的备用通道。
> 技术路线（用户拍板）：**两端出站 WSS 连 Cloudflare Worker，Worker + Durable Objects 做双向中转**，
> 流量即标准 HTTPS，自有域名，合规可审计。**不做域前置、不做协议伪装。**

---

## 1. 需求定义（纠正后的正式表述）

**紧急通道**：当客户机（Agent）与控制机（桌面 GUI 应用）之间的 Tailscale 直连不可用时，
通过 Cloudflare Workers（云端中转层）以标准 HTTPS/WSS 完成远程操作——下发指令、接收结果、
查看遥测、故障诊断。要求：

1. 两端都**主动出站**连接，天然穿透 NAT/防火墙（Agent 不需要公网 IP、不需要开端口）
2. 传输层就是标准 TLS/WSS，证书由 Cloudflare 自动签发，**与普通网页访问无差别**
3. 自有域名 + 全链路审计，**不需要也不允许任何伪装技术**
4. 自动切换：Tailscale 失效自动降级到 Worker 通道，恢复自动切回

---

## 2. 系统全景（三通路）

| 通路 | 传输 | 用途 | 状态 |
|---|---|---|---|
| ① Tailscale 主控 | WireGuard P2P mesh，控制机 GUI ↔ Agent **直连** | 完整功能（文件/命令/终端/VNC/日志/容器） | 主用 |
| ② Emergency（本文） | 控制机 GUI ↔ Worker(DO) ↔ Agent，**双向 WSS，出站连接** | Tailscale 不可达时的紧急/临时操作 | 本次设计 |
| ③ 飞书机器人 | Webhook + 事件订阅，Agent/控制面 → 飞书 | 24h 告警、定时上报、双向简单指令 | 另有开发 |

---

## 3. 紧急通道架构

```
┌────────────────────┐        ┌──────────────────────────────────┐        ┌────────────────────┐
│ 控制机 GUI (Tauri) │        │ Cloudflare Worker + Durable Object│        │ Agent (Orange Pi)  │
│ 出站 wss 连接      │◄──────►│ 按 node_id 分房间，双向转发        │◄──────►│ 出站 wss 连接       │
│ Bearer: GUI_TOKEN  │  TLS   │ 自动 TLS · 全球边缘节点就近接入    │  TLS   │ Bearer: AGENT_TOKEN │
└────────────────────┘        └──────────────────────────────────┘        └────────────────────┘
```

关键设计要点：

| 项 | 方案 |
|---|---|
| 接入方式 | 两端都是**出站** WSS 客户端，Worker 是唯一被动的服务端。Agent 在 NAT 后照连不误 |
| 房间模型 | **Durable Object 按 node_id 分房间**：Agent 连接（每节点唯一）+ 控制机连接（可多个）进同一房间，DO 负责转发 |
| 鉴权 | Workers Secrets 存 `AGENT_TOKEN` / `GUI_TOKEN`；握手 `Authorization: Bearer` + URL path 区分角色（`/agent?node_id=xx`、`/gui?node_id=xx`） |
| 心跳 | Agent 每 30s `ping`，DO 回 `pong`；60s 无响应判定离线，广播给在线 GUI |
| 离线暂存 | Agent 离线期间指令存 DO SQLite，Agent 上线后按 `seq` 补发（对齐"离线本地缓存 + 补传"理念） |
| 消息上限 | DO WS 单消息 32 MiB；终端/指令小帧无压力，大文件（>1MB）紧急通道分块传输或建议走主通道 |
| 计费 | Free：10 万请求/天；WS 建连 = 1 请求，消息 20:1 折算；心跳+遥测约 1.5 万/天/节点，余量充足 |

---

## 4. 传输层协议（WSS 帧）

统一 JSON 帧，语义镜像现有 proto 的 CommandRequest/CommandResult，**复用 Agent 已有 executor 与 commandType 白名单**，控制机侧不需要新协议解析器：

```jsonc
// 下行：GUI → Agent
{ "type": "command", "seq": 1024, "node_id": "n-xxx", "ts": 1725000000,
  "cmd": { "type": "SHELL", "params": { "command": "uptime" } } }

// 上行：Agent → GUI
{ "type": "result", "seq": 1024, "node_id": "n-xxx", "ts": 1725000001,
  "result": { "status": 1, "message": "ok", "stdout": "base64...", "privilege_hint": "" } }

// 遥测：Agent 定时 → DO → 广播 GUI
{ "type": "telemetry", "node_id": "n-xxx", "ts": 1725000000,
  "metrics": { "cpu": 12.3, "mem": 2048, "load1": 1.74, "temp": 56 } }

// 心跳/控制
{ "type": "ping" }             // Agent → DO，30s
{ "type": "pong" }             // DO → Agent
{ "type": "offline", "node_id": "n-xxx" }   // DO → 在线 GUI，Agent 掉线告警
```

**协议不变式**：
- `seq` 单调递增，收端去重、防重放
- `stdout` 沿用 base64（与现有 protojson 行为一致）
- `status` 沿用 ResultStatus 数字枚举（OK=1 FAILED=2 NEEDS_PRIVILEGE=3 TIMEOUT=4 REJECTED=5）——前端已踩过坑，本次直接数字判断

---

## 5. 自动切换（主备透明）

Agent 侧维护端点优先级表，控制机 GUI 相同逻辑：

```
端点表：[ tailscale://100.108.234.5:7443 (主), wss://ecp-relay.example.com (备) ]

主通道健康（心跳正常、RTT < 阈值）  → 只走 Tailscale
连续 3 次心跳超时 / TCP 断连         → 降级标记，切 Worker 通道
每 60s 探测一次主通道                 → 恢复即切回（hysteresis 防抖动）
```

- 切换对上层透明：GUI 的 `sendCommand()` 与遥测订阅只关心"当前通道"状态，不关心底层实现
- GUI 状态栏常显当前通道（Tailscale 直连 / Cloudflare 中转），触发切换时弹提示 + 写审计
- Agent 侧切换后立即上报 `channel: relay` 遥测，飞书可同步告警

---

## 6. 各端改造点

### 6.1 Cloudflare Worker（✅ 已实现，relay/ 目录）

| 文件 | 职责 | 状态 |
|---|---|---|
| `relay/src/index.ts` | 路由：`/agent`（AGENT_TOKEN）、`/gui`（GUI_TOKEN）、`/health`；Bearer 或 query token；常数时间比较；按 node_id 路由 DO 房间 | ✅ |
| `relay/src/room.ts` | DO 房间：Agent 唯一连接/多 GUI、双向转发、心跳判活（60s 超时 alarm 广播 offline）、离线指令 SQLite 暂存 + 上线按 seq 补发 | ✅ |
| `relay/wrangler.toml` | DO 绑定 `ROOMS` + SQLite migration + 自有域名注释 | ✅ |
| 部署 | 见下方 §6.4 | 待执行 |

**Worker 部署步骤（PowerShell，Windows）：**

```bash
cd D:/Users/flowe/WorkBuddy/边缘计算-算力节点/relay
npm install
npx wrangler login
npx wrangler secret put AGENT_TOKEN     # 生成强随机串：openssl rand -hex 24
npx wrangler secret put GUI_TOKEN
npx wrangler deploy                     # 首次出 workers.dev 子域
# 绑定自有域名：Cloudflare 控制面板 → Workers & Pages → ecp-relay →
#   Settings → Domains & Routes → Custom Domains → 添加 relay.<你的域名>
# 然后改 wrangler.toml 里 routes 注释并 re-deploy（或直接在面板绑定）
```

**Agent 侧启用（Pi 真机）：**
1. 把 `ECP_RELAY_TOKEN=<AGENT_TOKEN>` 写进 systemd 单元 Environment（或 `/etc/ecp/agent.yaml` 的 `relay.token`）
2. `agent.yaml` 改 `relay.enabled: true`、`relay.url: wss://relay.<你的域名>/agent`
3. `systemctl restart ecp-agent`（改配置后必须 restart，mv 换文件不生效是已知坑）

### 6.2 Agent（✅ 已实现，agent/internal/relay/）

| 模块 | 职责 | 状态 |
|---|---|---|
| `relay/relay.go` | 出站 wss（强制 wss 拒绝明文）、Bearer 鉴权、心跳 30s、重连退避 1s→60s+抖动、telemetry 10s | ✅ |
| `handleFrame` | 收 `command` 帧 → 复用现有 executor.Handle → 回 `result` 帧（stdout/stderr base64） | ✅ |
| `protoCommand` | 字符串类型名（SHELL/DOCKER_LIST/...）→ proto CommandType 映射 | ✅ |
| 接入 | `cmd/agent/main.go`：`cfg.Relay.Enabled` 时并发启动；`Transport.Exec()` 暴露复用 executor | ✅ |
| 编译 | 已 `go vet` + `go build ./...` + 交叉编译 linux/arm64 通过（26MB 单文件） | ✅ |

### 6.3 控制机 GUI（Tauri，前端改造）

| 模块 | 职责 | 状态 |
|---|---|---|
| `src/api/relay.ts` | WSS 客户端（浏览器原生 WebSocket）：连 `/gui`、重连、seq | 待做 |
| `src/api/client.ts` | execCommand 底层加通道抽象：优先 Tailscale 直连，失败自动切 relay | 待做 |
| UI | 状态栏显示当前通道；紧急模式禁用大文件传输 | 待做 |

### 6.4 InfinityFree 前端与本通道的关系（澄清）

InfinityFree 方案（前端静态托管）与本紧急通道**不冲突、可叠加**：
- InfinityFree 前端 → Cloudflare Tunnel → Pi 控制面（走完整 REST/gRPC）
- 本紧急通道 → Worker（DO 房间）→ 两端 WSS（仅当 Tailscale 直连不可用时兜底）
- 前端 `VITE_API_BASE` 若指向 Worker 域名，则 GUI/浏览器也能经 relay 通道操作（后续可做）

---

## 7. 部署说明（Worker）

```bash
# 本机（Windows，PowerShell）
cd .venv/relay          # 或直接用 npm 环境
npm i -g wrangler
wrangler login          # 浏览器授权你的 Cloudflare 账号

cd relay
npm install
wrangler secret put AGENT_TOKEN     # 生成强随机串
wrangler secret put GUI_TOKEN
wrangler deploy                     # 首次出 workers.dev 子域

# 绑定自有域名（控制面板 → Workers → 自定义域 → relay.example.com）
# DNS 由 Cloudflare 自动托管
```

**Free 额度核算**（1 个 Agent 估算）：
- 心跳 2880 条/天 + 遥测 8640 条/天 ≈ 1.15 万条，20:1 折算约 576 请求 + 建连若干
- 远低于 10 万/天上限；DO SQLite 暂存量极小（仅离线期间指令），Free 5GB 账户存储无压力

---

## 8. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| DO Free 计划限制（100 类/账号、SQLite 后端） | 多节点需共享类 | 单类多房间（node_id 分对象），类数不随节点增长 |
| workers.dev 子域被扫描 | 暴露面 | 自有域名 + Bearer 校验 + Worker 只回 101/健康检查，未授权不返回任何数据 |
| 自动切换抖动 | 指令漂移 | 连续 3 次失败才切 + 60s 探测恢复，hysteresis |
| 大文件经 WS | 32MiB 限制/浪费额度 | 紧急通道限制 payload ≤ 1MB，文件传输弹提示建议主通道 |
| Cloudflare 账号异常 | 通道不可用 | 归为"紧急通道降级"而非"系统故障"，控制机 GUI 本地缓存待重试 |

---

## 9. 验收标准

1. 关闭 Pi 的 Tailscale（或拔网模拟）：GUI 自动切到 relay，指令/遥测照常，状态栏显示"Cloudflare 中转"
2. 恢复 Tailscale：60s 内自动切回直连，无用户干预
3. 未授权 WS（无 token / 错误 node_id）被拒；日志可审计
4. Agent 离线期间 GUI 下发指令，上线后收到补发执行
5. 手机流量环境（纯公网、无 Tailscale）下 GUI 也能经 relay 连接 Agent

---

## 10. 待确认（实现前）

1. **控制机 GUI 框架**：Tauri（Rust + 复用现有 Vue 前端）还是 Qt？（影响打包与工具链）
2. **Agent 指令子集**：紧急通道复用现有 commandType **全集**（推荐，零新增）还是只暴露白名单子集（如 SHELL/STATUS/TAILSCALE_STATUS）？
3. **遥测落地**：relay 通道遥测是否入库（沿用现有 Telemetry 表），还是仅实时展示？
4. **与现有 server 的关系**：桌面 GUI 是**取代** server.exe（GUI 直连 Agent，server 退役），还是**并存**（GUI 也连 server 做审计中心）？