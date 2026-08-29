// Package executor 执行控制面下发的指令。
//
// 本期（T2）只实现 echo 桩，供 T3 联调：收到任意指令都回一个占位结果，
// 证明"控制面下发 → 节点回传 → 控制台展示"的链路通了。T4 会替换为真正的
// shell / 文件 / 网络 / 容器执行器。降级策略（需提权返回脚本）也会在 T4 实现。
package executor

import (
	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// Executor 执行指令。当前为 echo 桩。
type Executor struct{}

// New 构造执行器。
func New() *Executor { return &Executor{} }

// Handle 处理一条指令，返回执行结果。
//
// 设计约定：需要提权的操作不在本函数里自行 sudo，而是返回 NEEDS_PRIVILEGE
// 与一段可人工执行的脚本（T4 实现）。当前桩只回显指令类型。
func (e *Executor) Handle(cmd *ecpv1.Command) *ecpv1.CommandResult {
	return &ecpv1.CommandResult{
		TraceId: cmd.TraceId,
		Status:  ecpv1.ResultStatus_RESULT_STATUS_OK,
		Message: "echo stub: received " + cmd.Type.String(),
	}
}
