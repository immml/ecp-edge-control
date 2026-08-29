# 边缘计算控制平台 · T3 REST API + JWT/RBAC + 前端接通后端 · 完成概览

## 已完成（T3）

在 AGENTIC 模式下继续推进 **T3（REST API + 认证授权 + 前端接通后端）**，打通了「登录 → JWT → RBAC → 指令下发 → Agent 回执」的完整闭环。

### 新增/修改文件
- **`server/internal/auth/auth.go`**（新增）— JWT + RBAC 核心。`secret()` 读 `ECP_JWT_SECRET`（缺省回退开发常量）；`HashPassword`/`CheckPassword`（bcrypt）、`SignToken`（24h HS256）、`ParseToken`、`GenerateInitialPassword`（16 位随机 hex）、`RoleCan(role, required)` 等级判定（viewer=1/operator=2/admin=3）。
- **`server/internal/store/store.go`**（修改）— 新增 `GetUserByUsername` / `CreateUser` / `ListUsers` / `CountUsers` / `UpdateLastLogin`，支撑用户与登录。
- **`server/internal/session/session.go`**（修改）— 新增 `waiters map[string]chan *ecpv1.CommandResult` 与 `RegisterWaiter` / `CancelWaiter` / `DeliverResult`，把 Agent 回执按 `trace_id` 路由到等待中的下发协程。
- **`server/internal/grpcserver/server.go`**（修改）— `AgentMessage_Result` 分支调用 `DeliverResult`；暴露 `Sessions()`。
- **`server/internal/command/dispatcher.go`**（新增）— `Dispatch` 校验在线 → 生成 8 位 `trace_id` → 注册 waiter → 经会话下发 `ControlMessage_Command` → 落库 `model.Command`（pending）→ 等待 ≤30s（或 `TimeoutSec`）→ 写终态。离线返回 `ErrOffline`，超时返回 `ErrTimeout`。
- **`server/internal/api/api.go`**（新增）— gin 引擎。`POST /api/v1/login`、`GET /api/v1/me`、`GET /api/v1/nodes`、`GET /api/v1/nodes/:id`、`GET /api/v1/nodes/:id/telemetry`、`GET /api/v1/audit`、`POST /api/v1/nodes/:id/command`（需 operator）；统一 `{code,message,data}` 响应，错误码 `codeOK=0 / codeUnauth=10002 / codeForbidden=10003 / codeOffline=30001`；`JWTAuth` 中间件 + `RequireRole` 中间件；`commandType` 归一化枚举名。
- **`server/internal/web/web.go`** + `server/internal/web/dist/` — `//go:embed all:dist` 内嵌前端；SPA history 回退到 `index.html`。
- **`server/cmd/server/main.go`**（重写）— 集成 `bootstrapAdmin`（首启 `CountUsers==0` 生成 `admin` + 随机密码，仅打印一次）、拉起 gRPC、构建 dispatcher/api、gin `NoRoute` 托管内嵌前端、`ListenAndServeTLS` 用 CA 签发的服务端证书（`https://127.0.0.1:8443`）。
- **`agent/internal/transport/transport.go`**（修改）— `streamLoop` 收到 `ControlMessage_Command` 调 `handleCommand`，执行后回填 `TraceId` 回传 `AgentMessage_Result`。
- **`web/src/api/client.ts`**（新增）— 浏览器原生 `fetch` 客户端（无 axios）；`login/me/listNodes/getNode/audit/execCommand`，401 自动跳 `/login`，token 存 `ecp_token`。
- **`web/src/stores/auth.ts`**（新增）— 响应式鉴权 store（`init/login/logout`）。
- **`web/src/views/LoginView.vue`**（新增）— 登录表单。
- **`web/src/router/index.ts`**（修改）— 新增 `/login`，路由守卫（无 token→登录，已登录+`/login`→`/nodes`）。
- **`web/src/views/NodesView.vue` / `AuditView.vue`**（重写）— 接通真实 API，后端不可达时回退 mock 数据并提示「示例数据」（契合控制面非长驻模型）。

### 验证结果
- `server` / `agent` `go build ./...` 均通过；交叉编译 `ecp-agent-linux-arm64` / `amd64` 成功（CGO 静态）。
- 行为链路实测：无 token `/nodes` → 401；正确登录 → JWT；错误密码 → 401；鉴权后 `/me`、`/nodes`（空）、`/audit`（空）正常；向不存在节点下发 → `code:30001 节点离线`。
- 内嵌前端生效（标题「边缘节点控制台」，静态资源 200）。
- 首启 admin 密码仅打印一次（实测 `2c3f4a4d3726e0b1`），务必记录。

### 关键决策
- 控制面完全自包含：JWT 用 HS256 共享密钥（单机单 exe），未引入 Redis/OIDC。
- 前端用原生 `fetch`，避免引入 axios 依赖；离线回退 mock，适配「控制面按需上线」。
- 指令下发采用「trace_id + 等待者」模式，与 gRPC 异步回执解耦。

### 未闭环 / 后续
- 按「Git 编号提交」惯例提交 **#7**（本提交）。
- 推送 origin/main（#6 仍待 push）。
- **T4**（真实执行器、采集、告警）与 Orange Pi 真机端到端联调仍待做；真机命令需用户执行并贴回（本环境无法 SSH 远程执行）。
- 运维安全：`.workbuddy` 目录切勿删除；个人文件操作按高风险规则处理。
