# 边缘计算控制平台 · T2 传输层与注册鉴权 · 完成概览

## 已完成

在 AGENTIC 模式下继续推进了之前未完成的 **T2（传输层 + 注册鉴 权）**，这是 T3（控制面编排）与 T4（Agent 能力）的前置依赖闸门。

### 新增/修改文件
- **`server/internal/ca/ca.go`**（新增）— 内置 CA（Go 标准库 `crypto/x509`，零第三方 PKI）。`LoadOrCreate` 持久化 `ca.crt/ca.key` 到 `runtime/data/certs`；`SignClientCert`/`SignServerCert` 签发证书，CN 绑定 `node_id`。
- **`server/internal/grpcserver/server.go`**（新增）— gRPC 服务端。`Register` 用 `VerifyClientCertIfGiven` 放行（靠注册 Key + 硬件指纹鉴权），其余方法强制校验客户端证书 CN==node_id；`CommandStream` 处理心跳上行与下行回执；会话登记与在线判定。
- **`server/internal/session/session.go`**（新增）— 在线节点会话管理、心跳 TTL（90s）判定、最新遥测缓存。
- **`server/internal/store/store.go`**（修改）— 新增 `BindKeyToNode` / `IssueCredential` / `IsCertRevoked`，支撑"上线即控"绑定逻辑。
- **`server/cmd/server/main.go`**（修改）— 启动时生成/加载 CA 并拉起 gRPC（`:7443`），`init` 模式已验证建库 + 生成 CA。
- **`agent/internal/register/register.go`**（新增）— 私钥不出节点、生成 CSR、硬件指纹（machine-id + board-serial + MAC）、证书落盘。
- **`agent/internal/transport/transport.go`**（新增）— 端点探测、指数退避重连、CommandStream 心跳 + 能力上报、证书自动注册。
- **`agent/internal/executor/executor.go`**（新增）— echo 执行器桩，供 T3 联调。
- `agent/cmd/agent/main.go` 的 `run` 子命令接通传输层。
- 删除已废弃的 `worker/` , 目录（用户已确认砍掉 Cloudflare）。

### 验证结果
- `server` 与 `agent` 两个模块 **`go build ./...` 均通过**。
- 交叉编译产出：`server/dist/server.exe`（26MB）、`agent/dist/ecp-agent-linux-arm64`、`ecp-agent-linux-amd64`（各 ~25MB，`CGO_ENABLED=0` 静态）。
- `server.exe init` 实测：生成 `ca.crt/ca.key` 并完成 13 张表迁移。

### 关键决策
- 证书采用 ECDSA P256；CA 持久化以便控制面重启后节点无需重连。
- `Register` 是唯一免客户端证书的入口，其余方法强制 mTLS，契合架构 v2 §8.2。

### 未闭环 / 后续
- **T3（REST/JWT/RBAC/指令编排）**、**T4（真实执行器、采集、告警）** 尚未实现；前端仍是 mock 数据，未接后端。
- 真机端到端联调（注册 → 双向流心跳）需用户在 Orange Pi 上运行 agent 并把输出贴回（本环境无法 SSH 执行远程命令）。
- 模块依赖此前需切换 `GOPROXY=https://goproxy.cn,direct` + `GOSUMDB=off`（官方代理在本环境不可达）。

### Git
- 提交 **#6** `b4a82dc` — `feat(transport): 实现注册鉴权与 gRPC 传输层（T2） (#6)`，已落地本地（remote origin 待推送）。
