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
	// 密码（vncserver 以 root 跑 → /root/.vnc/passwd；免密 sudo 可用时能读到）
	if out, err := runBin(timeout, "sh", "-c",
		"sudo -n ls /root/.vnc/passwd >/dev/null 2>&1 && echo 1 || (ls $HOME/.vnc/passwd >/dev/null 2>&1 && echo 1 || echo 0)"); err == nil && strings.TrimSpace(string(out)) == "1" {
		st["password"] = true
	} else {
		st["hint"] = "VNC 密码未设置（启动时平台会要求设置）"
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
// 参数：
//   - geometry：分辨率（可选，默认 1280x800）
//   - password：VNC 访问密码（前端询问用户输入）
//   - sudo_password：节点 sudo 密码（可选，仅本次授权使用、不持久化；用于自动
//     配置 sudoers 免密 sudo + 设密码 + 启动，之后平台全自动）
// 实现：vncpasswd -f 写入密码（/root/.vnc/passwd）后启动 vncserver。
func (e *Executor) vncStart(cmd *ecpv1.Command) *ecpv1.CommandResult {
	timeout := dur(cmd.GetTimeoutSec(), 40)
	geometry := getString(cmd.GetParams(), "geometry")
	if geometry == "" {
		geometry = "1280x800"
	}
	password := getString(cmd.GetParams(), "password")
	if password == "" {
		password = "orangepi"
	}
	sudoPwd := getString(cmd.GetParams(), "sudo_password")
	setup := "mkdir -p /root/.vnc && echo '" + password + "' | vncpasswd -f > /root/.vnc/passwd && chmod 600 /root/.vnc/passwd"
	startCmd := "pkill -x Xvnc 2>/dev/null; sleep 1; vncserver -geometry " + geometry + " -alwaysshared"

	// 1) 免密 sudo：直接全自动
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

	// 2) 有 sudo 密码：自动授权（配置 sudoers 免密，仅本次使用密码、不持久化）+ 设密 + 启动
	if sudoPwd != "" {
		grant := "echo '" + sudoPwd + "' | sudo -S bash -c \"echo 'orangepi ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/ecp-agent && chmod 440 /etc/sudoers.d/ecp-agent\""
		if _, err := runBin(timeout, "bash", "-c", grant); err != nil {
			r := e.base(cmd)
			r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
			r.Message = "sudo 密码验证失败（" + err.Error() + "）"
			return r
		}
		out, err := runBin(timeout, "sudo", "bash", "-c", setup+" && "+startCmd)
		r := e.base(cmd)
		r.Stdout = out
		if err != nil {
			r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
			r.Message = err.Error()
			return r
		}
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
		r.Message = "VNC 已启动（端口 " + itoa(vncPortBase) + "，密码 " + password + "，已配置免密 sudo）"
		return r
	}

	// 3) 既无免密也无密码：返回手动授权脚本（兜底）+ 提示前端询问 sudo 密码
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
	r.PrivilegeHint = "需要节点 sudo 权限。更省事的做法：在前端弹窗输入一次 sudo 密码（仅本次授权使用，不保存），平台将自动配置免密 sudo 并完成启动。"
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
