# T4-D：Orange Pi 真机端到端联调报告

**日期**：2026-08-29 20:40（重启后复测完成）
**提交**：#10 `de45c48` + 日志补丁 `028d8a6`，**已推送 origin/main** ✅

## 一、结论

真机闭环「注册 → 心跳 → 能力上报 → 遥测 → 指令下发 → 真机执行 → 回执」**全部跑通**。Orange Pi 重启后 agent 重新拉起即自动重连（证书身份持久化生效，无 node_id mismatch），随后完成 live 指令复测。

## 二、最终实测结果（重启后）

| 项 | 结果 |
|---|---|
| agent 重启拉起 | ✅ 手动拉起（`nohup` 无自启；systemd 单元属后续项） |
| 重连 | ✅ 免注册直接带证书重连，`已连接到控制面`，无 mismatch |
| 节点在线 | ✅ API `/nodes` 返回 `n-f7f4f37a` status=`online` |
| 指令回执 | ✅ `POST /command` → `code:0`，`stdout`(base64) 解码：`ECP_OK / orangepi3b / 20:38:26 up 21 min, load 8.71`，耗时 38ms |
| 遥测恢复 | ✅ SQLite 持续新增样本（CPU 62%、温度 50°C、容器 1） |

## 三、发现并修复的 3 个 Bug（#10）

1. **Agent 并发 `stream.Recv()`**：原循环每 200ms 新开一个 `stream.Recv()` goroutine，违反 gRPC 单流并发 Recv 约束 → 流被破坏、指令收不到。改为**单一常驻接收协程**。
2. **NodeID 重启丢失**：`Identity.NodeID` 只存内存，重启后为空，心跳 `NodeId=""` 与证书 CN 不符 → 服务端拒绝 `node_id mismatch`。改为**从客户端证书 CN 回填**。
3. **指令参数结构**：`POST /command` 原要求 `params` 内嵌 `ecpv1.Command`（`{type, params:{params:{...}}}`），executor 收不到参数。简化为 `{type, params:{...}, timeout_sec}`。

另：server 新增 **`keygen` 子命令**（`server.exe keygen [label]`）签发注册 Key，明文打印、哈希入库。

## 四、遗留 / 后续

- **agent 无自启**：当前靠手动拉起。建议下一步给 Pi 装 systemd 单元（`ecp-agent.service`，`Restart=always`），实现真正的"上线即控"。
- **部署凭据**（SSH 密码、注册 Key `b45e704f...`）在 `.workbuddy/artifacts/deploy_pi.py`（项目数据目录，介意可删）。
- **后续阶段**：T4-D 完成后可推进前端遥测可视化、告警真机触发验证、systemd 自启、FRP 备用通道。
