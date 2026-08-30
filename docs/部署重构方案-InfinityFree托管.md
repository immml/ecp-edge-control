# ECP 控制面板部署重构方案 v1.0

> 文档日期：2026-08-30
> 规模评估：**不亚于重构**——控制面宿主迁移 + 前端部署方式变更 + 公网入口改造
> 前置文档：`InfinityFree部署评估报告.md`（能力边界与结论）

---

## 0. 一句话目标

**打开 InfinityFree 网址 = 直接看到控制面板（登录→节点→容器→终端→VNC→告警全可用）。**
本机不跑任何常驻进程；只保留：边缘节点（Orange Pi）、服务器（控制面也在 Pi 上）、浏览器。

---

## 1. 现状盘点（代码事实）

| 模块 | 现状 | 与目标的关系 |
|---|---|---|
| 前端 `web/src/api/client.ts` | `private base = '/'` **硬编码同源** | ❌ 必须改为可配置 |
| 前端终端/VNC | `location.host` 拼 WS 地址（`TerminalView.vue:34`、`VncView.vue:141`） | ❌ 必须改为可配置 |
| 前端路由 | `createWebHistory()`（`router/index.ts:69`） | ⚠️ InfinityFree 是 Apache，需 `.htaccess` 兜底 |
| 服务端 REST | `api.go` **无任何 CORS 中间件** | ❌ 跨域必炸，必须新增 |
| 服务端 WS | `terminal.go:20` `CheckOrigin: return true` 已放行 | ✅ 无需改 |
| 服务端监听 | HTTP `127.0.0.1:8443`、gRPC `0.0.0.0:7443` | ✅ 默认就安全（隧道回源 127.0.0.1） |
| 存储驱动 | `glebarez/sqlite`（纯 Go，modernc.org 系） | ✅ **CGO_ENABLED=0 可直接交叉编译 linux/arm64** |
| 证书 | 内置 CA 自签（`signed-server` 8760h） | ⚠️ 隧道用 http 回源即可绕开证书警告 |
| OTA 通告 | `main.go:153` 用 **`windows` 正则**探测 Tailscale IP | ⚠️ 跑 Linux 后探测不到，需改为显式配置 |

**四个关键坑（实施时必须处理）：**
1. **前端同源假设**：`base='/'` + `location.host`，前端一旦脱离控制面独立部署，全部 API/WS 失联
2. **server 无 CORS**：浏览器从 `.rf.gd` 页面跨域请求隧道域名，OPTIONS 预检直接 403
3. **history 路由**：InfinityFree 的 Apache 不会自己回退 `/nodes` 这类路径到 index.html
4. **自签证书**：浏览器访问隧道域名会告警；正确做法是隧道回源用 `http://127.0.0.1:8443`

---

## 2. 目标架构

```
┌────────────────────────────────────────────────────────┐
│ 浏览器（本机，零常驻）                                  │
│   └─ 打开 https://xxx.rf.gd  ← InfinityFree 前端 SPA  │
└──────────────────────┬─────────────────────────────────┘
                       │ /api/* 与 wss:// 跨域请求
┌──────────────────────▼─────────────────────────────────┐
│ Cloudflare Tunnel（免费，cloudflared 跑在 Pi 上）       │
│   公网 HTTPS 入口 → http://127.0.0.1:8443 回源          │
└──────────────────────┬─────────────────────────────────┘
┌──────────────────────▼─────────────────────────────────┐
│ Orange Pi 3B（Debian 12, aarch64）                      │
│   ├─ ecp-server（Go 控制面，127.0.0.1:8443 + 0.0.0.0:7443）│
│   │    └─ SQLite /opt/ecp-server/data/ecp.db            │
│   ├─ ecp-agent（同一节点，连 127.0.0.1:7443）           │
│   └─ cloudflared（隧道进程）                            │
└─────────────────────────────────────────────────────────┘
```

节点通信两条路径：
- **本机 Agent** → `127.0.0.1:7443`（同机，最安全）
- **未来多节点** → Tailscale `100.108.234.5:7443`（Pi 的 Tailscale IP，mesh 直连）

---

## 3. 改动清单

### 3.1 前端（web/）— 破解同源假设

| 文件 | 改动 | 要点 |
|---|---|---|
| `src/api/client.ts` | `base` 改读 `import.meta.env.VITE_API_BASE ?? '/'` | 保留空值回落 `/`，本地联调不受影响 |
| `src/views/TerminalView.vue:31-34` | WS 地址改用同一配置 | `const apiBase = import.meta.env.VITE_API_BASE ?? ''`，`${proto}://${apiHost}` |
| `src/views/VncView.vue:141` | 同上 | noVNC WS 与终端一致 |
| `src/vite-env.d.ts` | 补 `VITE_API_BASE` 类型声明 | `string` 可空 |
| `.env.infinityfree` | 新增构建模式 | `VITE_API_BASE=https://隧道域名` |
| `package.json` | 新增脚本 | `"build:if": "vite build --mode infinityfree"` |
| 产物 | `web/dist`（含 .htaccess） | 见 3.4 |

### 3.2 服务端（server/）— 跨域与 Linux 适配

| 文件 | 改动 | 要点 |
|---|---|---|
| `internal/api/api.go` | 新增 CORS 中间件 | 允许 Origin 白名单（`*.rf.gd`、隧道域名）或 `*`；处理 `OPTIONS` 预检；`Allow-Headers: Authorization, Content-Type`；`Allow-Credentials` 视 token 方案（我们用 Bearer header，可不开 credentials） |
| `cmd/server/main.go` | OTA 通告 IP 改为显式优先 + Linux 探测 | 删除 `windows` 正则硬编码，改为通用探测（`ip -4 addr` / Tailscale 状态解析），且 `advertise.endpoints` 配置必须写上 `100.108.234.5:7443` |
| `internal/config/config.go` | `Advertise.Endpoints` 默认值复用 | 确认 Linux 启动时不会因为探测失败打误导日志 |
| 服务端 WS 升级 | 无需改 | `CheckOrigin: return true` 已放行（配合 token 鉴权） |

### 3.3 Pi 部署（控制面 + 隧道）

| 项 | 内容 |
|---|---|
| 交叉编译 | `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/ecp-server-linux-arm64 ./cmd/server`（在 server/ 目录） |
| 部署目录 | `/opt/ecp-server/`（二进制 + config.yaml + data/） |
| systemd 服务 | `ecp-server.service`：`ExecStart=/opt/ecp-server/ecp-server -c /opt/ecp-server/config.yaml`，`Restart=always` |
| cloudflared | apt 装 `cloudflared`，`systemctl enable cloudflared`；quick tunnel 或命名 tunnel |
| 回源方式 | **`http://127.0.0.1:8443`**（绕开自签证书告警，隧道边缘负责 HTTPS） |
| 开机自启 | 两个 unit 都 `enable`；断网自动重连由 cloudflared 负责 |

### 3.4 InfinityFree 托管

| 项 | 内容 |
|---|---|
| 上传 | `web/dist/*` 整体传至 `htdocs/`（FTP 或文件管理器） |
| `.htaccess` | SPA fallback：`RewriteEngine On` + 非文件请求回退 `index.html`（InfinityFree 支持 .htaccess） |
| 免费子域名 | 控制面板建站后分配 `xxx.rf.gd` / `xxx.great-site.net` |
| 限额 | 3 万请求/天：SPA 静态资源小，控制台低频操作，够用 |
| 续期 | 免费子域名约 45 天需登录一次，列入日历 |

### 3.5 安全加固

| 层 | 措施 | 现状 |
|---|---|---|
| 认证 | JWT 登录（已有） | ✅ |
| 传输 | Cloudflare 边缘 TLS + 隧道 | ✅ 新增 |
| 前置防护 | Cloudflare Access（Zero Trust 免费 50 席位）在域名上再挡一道 | 可选 |
| 审计 | 控制面审计日志（已有） | ✅ |
| 暴露面 | server 只监听 127.0.0.1 供隧道回源，公网不直开任何端口 | ✅ 架构天然满足 |

---

## 4. 实施步骤（分阶段，每步可验证）

1. **前端改造（3.1）**：client.ts / Terminal / Vnc 支持 `VITE_API_BASE`；本地 `vite build` 验证回落 `/` 行为不变
2. **服务端改造（3.2）**：加 CORS；改 OTA 通告探测；本机（Windows）起服务验证现有功能不漏
3. **Pi 部署（3.3）**：交叉编译 → scp → systemd → 验证 `curl https://127.0.0.1:8443` 与 agent 同机注册
4. **隧道打通**：cloudflared quick tunnel → 浏览器直连隧道域名 → 登录 → 节点在线
5. **前端上线（3.4）**：`build:if` → 上传 InfinityFree → `.htaccess` → 打开 `xxx.rf.gd` 全流程走通
6. **安全收尾（3.5）**：可选用 Cloudflare Access；检查审计日志；确认本机无常驻进程

---

## 5. 验收标准

1. 全新浏览器打开 `xxx.rf.gd` → 登录页 → 登录 → 实时看到 Pi 的 CPU/内存/温度
2. 容器列表、批量指令、Tailscale/FRP 页、告警中心全部可用（全部走隧道）
3. 终端、VNC 正常（WebSocket 穿透隧道）
4. OTA 升级可用（二进制下载地址指向 `100.108.234.5:7443`）
5. 本机任务管理器无 ecp-server/agent 进程；Pi 断电重启后服务自动拉起
6. 手机浏览器（无自签信任）也能通过隧道域名正常访问

---

## 6. 风险与回滚

| 风险 | 影响 | 对策 |
|---|---|---|
| CORS 配置错误 | 全部接口跨域失败 | 先本地用 `vite --mode infinityfree` 连隧道域名联调，通过后再上线 |
| 隧道临时域名重启变化 | 前端 API 地址失效 | 用命名 tunnel + 自有域名（需用户提供）；临时域只做验证 |
| InfinityFree 限流/封号 | 前端打不开 | 前端纯静态，风险低；保留控制面直连（Tailscale 100.x）为兜底通道 |
| Pi 负载高（已有 wxedge） | 控制面响应慢 | Go 常驻内存占用小；必要时单独限流治理 |

**回滚方案**：前端回退由控制面托管（server 内置 `web.FS()` 仍在）；控制面代码改动保留向后兼容（`VITE_API_BASE` 空值回落 `/`）。双通道并行，随时可退回。

---

## 7. 待你补充/决策

1. **隧道域名**：有没有自有域名？（有 → 命名 tunnel 长期稳定；无 → 先用 trycloudflare 临时域验证）
2. **InfinityFree 账号**：你已有账号/子域名吗？还是需要走注册流程
3. **Cloudflare Zero Trust**：要不要加前置访问认证（推荐加，白名单你的邮箱）
4. **多节点预期**：未来是否有多台边缘节点？（影响 Advertise 配置与 gRPC 端口暴露策略）
5. 是否接受 **Agent 与控制面同机**（当前只有 1 台 Pi，天然如此）