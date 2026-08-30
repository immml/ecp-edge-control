# InfinityFree 部署评估报告

> 日期：2026-08-30
> 目标：打开 InfinityFree 网址就是控制面板；本机不做任何常驻；仅边缘节点(Pi) + 控制面 + 浏览器。

---

## 1. 结论先行

**能做，但必须拆两层，不能把整套控制面板丢进 InfinityFree。**

| 组件 | 放哪里 | 原因 |
|---|---|---|
| 前端 SPA（Vue 构建产物） | **InfinityFree**（用户指定入口） | 纯静态文件，InfinityFree 完全支持 |
| Go 控制面（API+gRPC+WebSocket+终端+VNC） | **Orange Pi 真机** | 长驻进程，必须真实服务器 |
| 公网入口（HTTPS 反代） | **Cloudflare Tunnel**（Pi 上跑 cloudflared，免费） | 把 Pi 的 8443 安全暴露成公网域名 |
| Agent（ecp-agent） | Orange Pi 本机 | 与控制面同机，连 127.0.0.1，最安全 |

浏览器打开 `你的子域名.rf.gd` → 加载 SPA 界面 → 前端 JS 把 API 请求发到 Cloudflare Tunnel 域名 → 隧道转发到 Pi 上的 Go 控制面 8443 → 返回数据。**本机全程零进程**。

---

## 2. 为什么不能把控制面板整个放 InfinityFree

InfinityFree 是 iFastNet 的免费共享主机，2026 年实测能力：

| 能力 | InfinityFree | 我们的控制面需求 |
|---|---|---|
| PHP 8.3 / 静态文件 | ✅ | 前端静态资源 ✅ 可满足 |
| MySQL | ✅ | 我们用 SQLite 本地文件 |
| Node.js / Python 运行时 | ❌ | 前端构建期用，运行期不需要 |
| SSH / 命令行 | ❌ | 无法部署/诊断 |
| **长驻进程 / 后台任务 / cron** | ❌ | **控制面必须长驻，致命** |
| **WebSocket 服务端** | ❌ | 终端、VNC、实时遥测全要 WS |
| **gRPC / TCP 双向流** | ❌ | Agent 回连控制面靠 gRPC |
| 请求量 | 上限 3 万次/天 | 控制台低频使用，够用 |
| inode | 上限 3 万 | SPA 几百个文件，够用 |

**一句话：InfinityFree 再强也只是"网页托管"，不是"服务器"。控制面这种要常驻、要双向流、要 WebSocket 的东西，它装不下。** 这跟"Windows 开发机没装 Docker 所以 PG/Redis 全砍"是同一类现实约束。

---

## 3. 目标架构

```
浏览器（本机，零常驻）
   │  打开 https://xxx.rf.gd  ← InfinityFree：只放前端静态文件
   ▼
InfinityFree 免费子域名（前端 SPA）
   │  JS 发起 /api 请求，API 地址指向 ↓
   ▼
Cloudflare Tunnel（免费，cloudflared 跑在 Pi 上）
   │  HTTPS 公网入口，自动 TLS + 可选 Zero Trust 前置认证
   ▼
Orange Pi 3B（192.168.1.5 内网，100.108.234.5 Tailscale）
   ├─ ecp-server（Go 控制面，监听 127.0.0.1:8443）
   │     └─ SQLite（/opt/ecp-server/data）
   └─ ecp-agent ↔ 控制面走 127.0.0.1（同机，不暴露公网）
```

数据流三层，每一层都是标准做法：
1. **InfinityFree** 只服务静态文件（index.html + assets/），请求量极小，不会触发 3 万/天上限
2. **Cloudflare Tunnel** 免费、免公网 IP、自动证书，用户已有 Cloudflare 账号（记忆：有 Workers）
3. **Pi** 上控制面只绑 127.0.0.1，公网只通过隧道访问——比直开端口安全得多

---

## 4. 需要改动的工作量

| 改动 | 内容 | 工作量 |
|---|---|---|
| ① 前端支持外部 API 地址 | `client.ts` 的 `base='/'` 改为读 `import.meta.env.VITE_API_BASE`，无配置时回落 `/` | 小（约 20 行） |
| ② 单独构建 SPA | `vite build --mode infinityfree`，产物上传 InfinityFree htdocs | 小 |
| ③ Pi 交叉编译 server | `GOOS=linux GOARCH=arm64` 出 linux 版（现在只有 server.exe） | 小 |
| ④ Pi 部署控制面 | 目录 `/opt/ecp-server` + systemd 常驻 + SQLite 数据目录 | 中 |
| ⑤ cloudflared 隧道 | Pi 装 cloudflared，建 Tunnel 指向 `http://127.0.0.1:8443` | 小 |
| ⑥ 安全加固 | JWT 登录已有；可选 Cloudflare Access（Zero Trust 免费 50 席位）在前置一道 | 可选 |

**预计不改任何 Agent 代码、不动 gRPC 协议、不碰核心架构。**

---

## 5. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| InfinityFree 免费子域名需定期登录续期（约 45 天） | 域名失效 | 日历提醒；或绑定自有域名（CNAME 到隧道） |
| 3 万请求/天上限 | 高频刷新会触发 | 控制台低频使用，实测无压力；可加 Cloudflare CDN 缓存静态资源 |
| 控制面暴露公网 | 被扫描/爆破 | 只经隧道访问 + JWT 登录 + 可选 Zero Trust 前置认证 + 审计日志（已有） |
| Pi 负载已偏高（8.11，wxedge 业务） | 控制面挤占资源 | Go 千字节级常驻内存占用，实测后再定；必要时限内存 |
| 无自有域名 | 隧道域名是 trycloudflare 临时域（重启会变） | 有域名最好；没有先临时域演示，后续 CNAME 绑定 |

---

## 6. 验收标准

1. 手机/任何浏览器打开 `xxx.rf.gd` → 直接看到登录页 → 登录后看到节点列表
2. 能实时看到 Pi 的 CPU/内存/温度（走隧道到控制面）
3. 终端、VNC 可用（WebSocket 穿透隧道正常）
4. 本机无任何常驻进程；断网恢复后 Pi 上控制面 + agent 自动拉起
5. 所有操作写入审计日志

---

## 7. 决策点（需要你拍板）

- **A. 按本方案执行**：前端→InfinityFree，控制面→Pi，隧道→Cloudflare Tunnel
- **B. 只要前端演示**：只传 SPA 到 InfinityFree，API 先指本地调试（外网连不上，只能看界面）
- **C. 换全家桶 Cloudflare**：前端也放 Cloudflare Pages，InfinityFree 只留跳转页（不推荐，你点名要 InfinityFree）

我推荐 **A**，与你的诉求完全吻合。