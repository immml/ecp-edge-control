// Package executor 包含 VNC 远程桌面命令（VNC_STATUS/START/STOP）的实现。
//
// 状态：UI 入口已撤回（边缘节点为无头后端，不需要 VNC 远程桌面）。
// agent/server 端命令实现保留作为底座（PROTO_COMMAND_TYPE_VNC_*），
// 将来若需要远程桌面/调测时只需重新接入 Web UI（VncView.vue + noVNC）。
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
	// 密码（tigervnc 用户目录）
	if out, err := runBin(timeout, "sh", "-c",
		"ls $HOME/.vnc/passwd >/dev/null 2>&1 && echo 1 || echo 0"); err == nil && strings.TrimSpace(string(out)) == "1" {
		st["password"] = true
	} else {
		st["hint"] = "VNC 密码未设置（首次启动需要 vncpasswd）"
	}
	data, _ := json.Marshal(st)
	r := e.base(cmd)
	r.Stdout = data
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Message = "ok"
	return r
}

// vncStart 启动 VNC server。
//
// 参数：geometry（可选，默认 1280x800）。首次启动若无密码则提示先设 vncpasswd。
// 需要 root/用户级 vncserver：有免密 sudo 则直接执行，否则降级脚本。
func (e *Executor) vncStart(cmd *ecpv1.Command) *ecpv1.CommandResult {
	timeout := dur(cmd.GetTimeoutSec(), 30)
	geometry := getString(cmd.GetParams(), "geometry")
	if geometry == "" {
		geometry = "1280x800"
	}
	// 检查密码
	hasPwd := false
	if out, err := runBin(timeout, "sh", "-c", "ls $HOME/.vnc/passwd >/dev/null 2>&1 && echo 1 || echo 0"); err == nil && strings.TrimSpace(string(out)) == "1" {
		hasPwd = true
	}
	script := "vncserver -geometry " + geometry + " -localhost no -AlwaysShared"
	if !hasPwd {
		r := e.base(cmd)
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
		r.PrivilegeScript = "#!/bin/bash\nset -euo pipefail\nvncpasswd  # 按提示设置 VNC 密码（必做一次）\n" + script + "\n"
		r.PrivilegeHint = "VNC 密码未设置，请在节点上先执行 vncpasswd 设密码，再启动"
		return r
	}
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
		r.Message = "VNC 已启动（端口 " + itoa(vncPortBase) + "）"
		return r
	}
	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
	r.PrivilegeScript = "#!/bin/bash\nset -euo pipefail\n" + script + "\n"
	r.PrivilegeHint = "启动 VNC 需要权限，请人工 sudo 执行以下脚本"
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
