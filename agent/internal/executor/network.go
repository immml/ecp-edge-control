package executor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// tailscaleStatus 查询 Tailscale 状态（只读，普通用户可执行）。
func (e *Executor) tailscaleStatus(cmd *ecpv1.Command) *ecpv1.CommandResult {
	timeout := dur(cmd.GetTimeoutSec(), 15)
	out, err := runBin(timeout, "tailscale", "status")
	r := e.base(cmd)
	r.Stdout = out
	if err != nil {
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
		r.Message = err.Error()
		return r
	}
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Message = "ok"
	return r
}

// tailscaleUp 启动 Tailscale 并登录（需 root）。非 root 时返回人工 sudo 脚本。
//
// 参数：authkey（可选，设备授权密钥）；hostname（可选，自定义节点名）。
func (e *Executor) tailscaleUp(cmd *ecpv1.Command) *ecpv1.CommandResult {
	if osGeteuid() != 0 {
		authkey := getString(cmd.GetParams(), "authkey")
		hostname := getString(cmd.GetParams(), "hostname")
		var b strings.Builder
		b.WriteString("#!/bin/bash\nset -euo pipefail\n")
		b.WriteString("tailscale up")
		if authkey != "" {
			b.WriteString(" --authkey " + authkey)
		}
		if hostname != "" {
			b.WriteString(" --hostname " + hostname)
		}
		b.WriteString("\ntailscale status\n")
		r := e.base(cmd)
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
		r.PrivilegeScript = b.String()
		r.PrivilegeHint = "Tailscale 控制需要 root，请人工 sudo 执行以下脚本"
		return r
	}
	args := []string{"up"}
	if v := getString(cmd.GetParams(), "authkey"); v != "" {
		args = append(args, "--authkey", v)
	}
	if v := getString(cmd.GetParams(), "hostname"); v != "" {
		args = append(args, "--hostname", v)
	}
	timeout := dur(cmd.GetTimeoutSec(), 30)
	out, err := runBin(timeout, "tailscale", args...)
	r := e.base(cmd)
	r.Stdout = out
	if err != nil {
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
		r.Message = err.Error()
		return r
	}
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Message = "tailscale up 完成"
	return r
}

// tailscaleDown 停用 Tailscale（需 root）。非 root 时返回人工 sudo 脚本。
func (e *Executor) tailscaleDown(cmd *ecpv1.Command) *ecpv1.CommandResult {
	if osGeteuid() != 0 {
		r := e.base(cmd)
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
		r.PrivilegeScript = "#!/bin/bash\nset -euo pipefail\ntailscale down\ntailscale status\n"
		r.PrivilegeHint = "Tailscale 控制需要 root，请人工 sudo 执行以下脚本"
		return r
	}
	timeout := dur(cmd.GetTimeoutSec(), 20)
	out, err := runBin(timeout, "tailscale", "down")
	r := e.base(cmd)
	r.Stdout = out
	if err != nil {
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
		r.Message = err.Error()
		return r
	}
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Message = "tailscale down 完成"
	return r
}

// frpStatus 检测节点 frpc 状态：二进制/进程/配置/systemd 单元，输出 JSON。
func (e *Executor) frpStatus(cmd *ecpv1.Command) *ecpv1.CommandResult {
	timeout := dur(cmd.GetTimeoutSec(), 15)
	binPath := ""
	if out, err := runBin(timeout, "sh", "-c", "command -v frpc || ls /usr/local/bin/frpc /usr/bin/frpc 2>/dev/null | head -1"); err == nil {
		binPath = strings.TrimSpace(string(out))
	}
	procRunning := false
	if out, err := runBin(timeout, "sh", "-c", "pgrep -x frpc >/dev/null && echo 1 || echo 0"); err == nil {
		procRunning = strings.TrimSpace(string(out)) == "1"
	}
	// 常见配置路径
	confPath := ""
	for _, p := range []string{"/etc/frp/frpc.toml", "/etc/frpc.toml", "/etc/frpc.ini", "/home/orangepi/frpc.toml"} {
		if out, err := runBin(timeout, "sh", "-c", "[ -f "+p+" ] && echo 1 || echo 0"); err == nil && strings.TrimSpace(string(out)) == "1" {
			confPath = p
			break
		}
	}
	svc := ""
	if out, err := runBin(timeout, "sh", "-c", "systemctl is-active frpc 2>/dev/null || echo inactive"); err == nil {
		svc = strings.TrimSpace(string(out))
	}
	state := map[string]any{
		"bin":        binPath,
		"running":    procRunning,
		"config":     confPath,
		"service":    svc,
		"configured": confPath != "",
	}
	data, _ := json.Marshal(state)
	r := e.base(cmd)
	r.Stdout = data
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Message = "ok"
	return r
}

// frpUp 启动 frpc（需 root）。非 root 时返回人工 sudo 脚本。
func (e *Executor) frpUp(cmd *ecpv1.Command) *ecpv1.CommandResult {
	if osGeteuid() != 0 {
		r := e.base(cmd)
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
		r.PrivilegeScript = `#!/bin/bash
set -euo pipefail
if systemctl list-unit-files frpc.service >/dev/null 2>&1; then
  systemctl enable --now frpc
else
  nohup frpc -c /etc/frp/frpc.toml >/var/log/frpc.log 2>&1 &
fi
sleep 2
pgrep -x frpc && echo "frpc running"
`
		r.PrivilegeHint = "启动 frpc 需要 root，请人工 sudo 执行以下脚本"
		return r
	}
	timeout := dur(cmd.GetTimeoutSec(), 30)
	out, err := runBin(timeout, "sh", "-c", "systemctl start frpc 2>/dev/null || (nohup frpc -c /etc/frp/frpc.toml >/var/log/frpc.log 2>&1 &)")
	r := e.base(cmd)
	r.Stdout = out
	if err != nil {
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
		r.Message = err.Error()
		return r
	}
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Message = "frpc 已启动"
	return r
}

// frpDown 停止 frpc（需 root）。非 root 时返回人工 sudo 脚本。
func (e *Executor) frpDown(cmd *ecpv1.Command) *ecpv1.CommandResult {
	if osGeteuid() != 0 {
		r := e.base(cmd)
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
		r.PrivilegeScript = `#!/bin/bash
set -euo pipefail
systemctl stop frpc 2>/dev/null || pkill -x frpc || true
sleep 1
pgrep -x frpc >/dev/null && echo "still running" || echo "frpc stopped"
`
		r.PrivilegeHint = "停止 frpc 需要 root，请人工 sudo 执行以下脚本"
		return r
	}
	timeout := dur(cmd.GetTimeoutSec(), 20)
	out, err := runBin(timeout, "sh", "-c", "systemctl stop frpc 2>/dev/null || pkill -x frpc || true")
	r := e.base(cmd)
	r.Stdout = out
	if err != nil {
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
		r.Message = err.Error()
		return r
	}
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Message = "frpc 已停止"
	return r
}

// runBin 执行外部命令（无 shell 拼接），返回 stdout 与 error。
func runBin(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.Stderr, err
		}
		return nil, err
	}
	return out, nil
}

// osGeteuid 返回当前有效用户 ID（便于测试注入）。
var osGeteuid = func() int { return os.Geteuid() }
