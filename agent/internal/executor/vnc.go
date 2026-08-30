package executor

import (
	"encoding/json"
	"strings"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// vncPorts 常见 VNC 显示号映射：display N -> 5900+N
const vncPortBase = 5900

// vncStatus 探测节点 VNC：二进制/监听端口/密码是否已设置，输出 JSON。
func (e *Executor) vncStatus(cmd *ecpv1.Command) *ecpv1.CommandResult {
	timeout := dur(cmd.GetTimeoutSec(), 15)
	st := map[string]any{
		"installed": false,
		"running":   false,
		"port":      -1,
		"password":  false,
		"binary":    "",
		"hint":      "",
	}
	// 二进制
	for _, b := range []string{"vncserver", "Xvnc", "x11vnc"} {
		if out, err := runBin(timeout, "sh", "-c", "command -v "+b); err == nil && strings.TrimSpace(string(out)) != "" {
			st["installed"] = true
			st["binary"] = strings.TrimSpace(string(out))
			break
		}
	}
	if st["installed"] == false {
		st["hint"] = "节点未安装 VNC（需要 tigervnc-standalone-server 等）"
		data, _ := json.Marshal(st)
		r := e.base(cmd)
		r.Stdout = data
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
		r.Message = "ok"
		return r
	}
	// 监听端口
	if out, err := runBin(timeout, "sh", "-c",
		"ss -tlnp 2>/dev/null | grep -oE ':(590[0-9]) ' | head -1 | tr -d ': '"); err == nil && strings.TrimSpace(string(out)) != "" {
		port := strconvAtoi(strings.TrimSpace(string(out)))
		st["running"] = true
		st["port"] = port
	}
	// 密码（root 或普通用户目录，vncserver 以 root 跑时在 /root/.vnc）
	if out, err := runBin(timeout, "sh", "-c",
		"ls /root/.vnc/passwd $HOME/.vnc/passwd >/dev/null 2>&1 && echo 1 || echo 0"); err == nil && strings.TrimSpace(string(out)) == "1" {
		st["password"] = true
	} else {
		st["hint"] = "VNC 密码未设置（平台会自动设为 orangepi）"
	}
	data, _ := json.Marshal(st)
	r := e.base(cmd)
	r.Stdout = data
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Message = "ok"
	return r
}

// vncStart 启动 VNC server（自动设密码 + 启动，一条链路）。
//
// 参数：geometry（可选，默认 1280x800）、password（可选，默认 orangepi）。
// 实现：vncpasswd -f 写入密码（root 用户目录 /root/.vnc/passwd）后启动 vncserver。
// 提权：节点有免密 sudo 则全自动；否则返回一次性授权脚本（自动配置 sudoers
// NOPASSWD + 设密码 + 启动），用户 sudo 执行一次后平台全自动。
func (e *Executor) vncStart(cmd *ecpv1.Command) *ecpv1.CommandResult {
	timeout := dur(cmd.GetTimeoutSec(), 30)
	geometry := getString(cmd.GetParams(), "geometry")
	if geometry == "" {
		geometry = "1280x800"
	}
	password := getString(cmd.GetParams(), "password")
	if password == "" {
		password = "orangepi"
	}
	setup := "mkdir -p /root/.vnc && echo '" + password + "' | vncpasswd -f > /root/.vnc/passwd && chmod 600 /root/.vnc/passwd"
	startCmd := "vncserver -geometry " + geometry + " -AlwaysShared"

	if sudoOK() {
		out, err := runBin(timeout, "sudo", "bash", "-c", setup+" && "+startCmd)
		r := e.base(cmd)
		r.Stdout = out
		if err != nil {
			r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
			r.Message = err.Error()
			return r
		}
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
		r.Message = "VNC 已启动（端口 " + itoa(vncPortBase) + "，密码 " + password + "）"
		return r
	}

	// 无免密 sudo：返回一次性授权脚本（含 sudoers 配置 + 设密码 + 启动）
	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
	r.PrivilegeScript = `#!/bin/bash
set -euo pipefail
# 1) 一次性授权：之后平台对节点的提权操作（VNC/frpc/tailscale 启停、写配置）全自动。
#    如需取消授权：rm /etc/sudoers.d/ecp-agent
echo "orangepi ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/ecp-agent
chmod 440 /etc/sudoers.d/ecp-agent
# 2) 设置 VNC 密码
` + setup + `
# 3) 启动 VNC
` + startCmd + `
echo "VNC 已启动，端口 5900"`
	r.PrivilegeHint = "首次启动请在节点 sudo 执行一次（自动配置免密 sudo + VNC 密码设为 orangepi），之后平台全自动"
	return r
}

// vncStop 停止 VNC server（vncserver -kill :1）。
func (e *Executor) vncStop(cmd *ecpv1.Command) *ecpv1.CommandResult {
	timeout := dur(cmd.GetTimeoutSec(), 20)
	display := getString(cmd.GetParams(), "display")
	if display == "" {
		display = ":1"
	}
	script := "vncserver -kill " + display + " 2>/dev/null || true"
	if sudoOK() {
		out, err := runBin(timeout, "sudo", "bash", "-c", script)
		r := e.base(cmd)
		r.Stdout = out
		if err != nil {
			r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
			r.Message = err.Error()
			return r
		}
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
		r.Message = "VNC 已停止"
		return r
	}
	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
	r.PrivilegeScript = "#!/bin/bash\nset -euo pipefail\n" + script + "\n"
	r.PrivilegeHint = "停止 VNC 需要权限，请人工 sudo 执行以下脚本"
	return r
}

// strconvAtoi 简易字符串转 int（避免引入 strconv 冲突）。
func strconvAtoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
