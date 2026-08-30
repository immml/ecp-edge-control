// Package executor 执行控制面下发的指令。
//
// T4-A：把 T2 的 echo 桩替换为真实执行器。设计原则是"能力探测先行、运行时判定"：
//   - Shell：直接执行命令，非 root 且控制面已声明需要特权时降级为"生成脚本 + 人工 sudo"
//     （架构硬约束：Agent 默认非 root，提权操作不自行 sudo）。进程 / systemd 管理
//     也走 shell 路径（ps / systemctl / pgrep），是 Linux 上最自然的方式。
//   - Docker：受能力探测分级约束——读操作需 CanReadDocker，写操作（start/stop/restart）
//     需 CanWriteDocker（即在 docker 组）；写操作只作用于带 ecp.managed 标签的容器，
//     这是隔离红线的落地。
//
// 所有外部命令都有超时保护，绝不能让一条指令把 Agent 卡死。
package executor

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/agent/internal/caps"
	"ecp.dev/ecp/agent/internal/config"
)

// Executor 是真实的指令执行器。
type Executor struct {
	caps *caps.Set
	cfg  *config.Config
}

// New 构造执行器。能力探测在启动时做一次；权限通常稳定，足够覆盖多数场景。
func New(cfg *config.Config) *Executor {
	return &Executor{caps: caps.Probe(), cfg: cfg}
}

// Handle 按指令类型分发到对应的真实执行器。
func (e *Executor) Handle(cmd *ecpv1.Command) *ecpv1.CommandResult {
	start := time.Now()

	res := func() *ecpv1.CommandResult {
		switch cmd.GetType() {
		case ecpv1.CommandType_COMMAND_TYPE_SHELL:
			return e.execShell(cmd)
		case ecpv1.CommandType_COMMAND_TYPE_DOCKER_LIST:
			return e.dockerList(cmd)
		case ecpv1.CommandType_COMMAND_TYPE_DOCKER_ACTION:
			return e.dockerAction(cmd)
		case ecpv1.CommandType_COMMAND_TYPE_DOCKER_LOGS:
			return e.dockerLogs(cmd)
		case ecpv1.CommandType_COMMAND_TYPE_FILE_LIST:
			return e.fileList(cmd)
		case ecpv1.CommandType_COMMAND_TYPE_TAILSCALE_STATUS:
			return e.tailscaleStatus(cmd)
		case ecpv1.CommandType_COMMAND_TYPE_TAILSCALE_UP:
			return e.tailscaleUp(cmd)
		case ecpv1.CommandType_COMMAND_TYPE_TAILSCALE_DOWN:
			return e.tailscaleDown(cmd)
		case ecpv1.CommandType_COMMAND_TYPE_FRP_STATUS:
			return e.frpStatus(cmd)
		case ecpv1.CommandType_COMMAND_TYPE_FRP_UP:
			return e.frpUp(cmd)
		case ecpv1.CommandType_COMMAND_TYPE_FRP_DOWN:
			return e.frpDown(cmd)
		default:
			return e.fail(cmd, "不支持的指令类型: "+cmd.GetType().String())
		}
	}()

	res.TraceId = cmd.GetTraceId()
	res.DurationMs = time.Since(start).Milliseconds()
	return res
}

// execShell 执行 shell 命令。
//
// 参数：params.command 或 params.script（脚本形式）。
// 特权策略：控制面已声明 requires_privilege 且当前非 root → 返回 NEEDS_PRIVILEGE，
// 附带可直接 sudo 执行的脚本，交由人工确认执行，绝不自行提权。
func (e *Executor) execShell(cmd *ecpv1.Command) *ecpv1.CommandResult {
	cmdStr := getString(cmd.GetParams(), "command")
	if cmdStr == "" {
		cmdStr = getString(cmd.GetParams(), "script")
	}
	if strings.TrimSpace(cmdStr) == "" {
		return e.fail(cmd, "缺少 command / script 参数")
	}

	// 提权降级：非 root 且控制面判定需要特权 → 生成脚本交人工 sudo。
	if cmd.GetRequiresPrivilege() && os.Geteuid() != 0 {
		script := "#!/bin/bash\nset -euo pipefail\n" + cmdStr + "\n"
		r := e.base(cmd)
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
		r.PrivilegeScript = script
		r.PrivilegeHint = "需要 root 权限，请人工 sudo 执行（已生成脚本）"
		return r
	}

	timeout := time.Duration(cmd.GetTimeoutSec()) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	//nolint:gosec // 受控指令：command 来自控制面且需授权后才下发，按设计执行本地命令。
	proc := exec.CommandContext(ctx, "bash", "-c", cmdStr)
	var stdout, stderr bytes.Buffer
	proc.Stdout = &stdout
	proc.Stderr = &stderr
	err := proc.Run()

	r := e.base(cmd)
	r.Stdout = stdout.Bytes()
	r.Stderr = stderr.Bytes()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			r.Status = ecpv1.ResultStatus_RESULT_STATUS_TIMEOUT
			r.Message = "指令执行超时"
			return r
		}
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
		if r.Message == "" {
			r.Message = err.Error()
		}
	}
	if proc.ProcessState != nil {
		r.ExitCode = int32(proc.ProcessState.ExitCode())
	}
	if r.Status == ecpv1.ResultStatus_RESULT_STATUS_UNSPECIFIED {
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
		r.Message = "ok"
	}
	return r
}

// base 构造一个带 trace、默认 OK 的空结果。
func (e *Executor) base(cmd *ecpv1.Command) *ecpv1.CommandResult {
	return &ecpv1.CommandResult{TraceId: cmd.GetTraceId()}
}

// fail 构造一个失败的指令结果。
func (e *Executor) fail(cmd *ecpv1.Command, msg string) *ecpv1.CommandResult {
	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
	r.Message = msg
	return r
}

// getString 从 structpb 参数中取字符串字段。
func getString(p *structpb.Struct, key string) string {
	if p == nil {
		return ""
	}
	if v, ok := p.GetFields()[key]; ok {
		return v.GetStringValue()
	}
	return ""
}

// getInt 从 structpb 参数中取数值字段。
func getInt(p *structpb.Struct, key string) int {
	if p == nil {
		return 0
	}
	if v, ok := p.GetFields()[key]; ok {
		return int(v.GetNumberValue())
	}
	return 0
}
