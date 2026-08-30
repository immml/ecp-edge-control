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

// frpConfCandidates 返回候选配置文件路径（ini 优先，多实例场景按文件枚举）。
func frpConfCandidates() []string {
	return []string{
		"/etc/frp/frpc.ini",
		"/etc/frpc.ini",
		"/etc/frp/frpc.toml",
		"/etc/frpc.toml",
	}
}

// sudoOK 探测当前用户是否拥有免密 sudo（运行时判定，非编译期假设）。
func sudoOK() bool {
	out, err := runBin(5*time.Second, "sudo", "-n", "true")
	return err == nil && strings.TrimSpace(string(out)) == ""
}

// frpStatus 枚举节点上全部 frpc 实例：systemd 单元 + 进程 + 配置文件，输出 JSON 数组。
//
// 多服务商场景（ChmlFrp / HayFRP 等）每套 frpc 作为一个实例，前端分别管理。
func (e *Executor) frpStatus(cmd *ecpv1.Command) *ecpv1.CommandResult {
	timeout := dur(cmd.GetTimeoutSec(), 15)
	instances := []map[string]any{}
	seen := map[string]bool{}

	// 1) systemd 单元：frpc.service / frpc-<name>.service / frpc@<name>.service
	if out, err := runBin(timeout, "sh", "-c",
		"systemctl list-unit-files 'frpc*' --no-legend 2>/dev/null | awk '{print $1}'"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			unit := strings.TrimSpace(line)
			if unit == "" {
				continue
			}
			name := strings.TrimSuffix(unit, ".service")
			// 归一化：frpc-xxx / frpc@xxx / frpc -> 实例名 xxx / frpc
			name = strings.TrimPrefix(strings.ReplaceAll(name, "@", "-"), "frpc-")
			if name == "" || name == "frpc" {
				name = "default"
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			enabled := ""
			if o, err := runBin(timeout, "sh", "-c", "systemctl is-enabled "+unit+" 2>/dev/null"); err == nil {
				enabled = strings.TrimSpace(string(o))
			}
			instances = append(instances, map[string]any{
				"name":     name,
				"source":   "systemd",
				"unit":     unit,
				"running":  enabled == "enabled" || enabled == "static",
				"enabled":  enabled,
				"bin":      "",
				"cmdline":  "",
				"config":   "",
				"configured": false,
			})
		}
	}

	// 2) 运行中的 frpc 进程（可能不在 systemd 里，如 ChmlFrp -u/-p 模式）
	if out, err := runBin(timeout, "sh", "-c", "ps -eo pid=,args= | grep '[f]rpc'"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, " ", 2)
			if len(parts) != 2 {
				continue
			}
			cmdline := strings.TrimSpace(parts[1])
			name := "proc-" + strings.TrimSpace(parts[0])
			if seen[name] {
				continue
			}
			seen[name] = true
			bin := ""
			if o, err := runBin(timeout, "sh", "-c", "readlink -f $(echo '"+strings.ReplaceAll(cmdline, "'", "")+"' | awk '{print $1}') 2>/dev/null || echo ''"); err == nil {
				bin = strings.TrimSpace(string(o))
			}
			instances = append(instances, map[string]any{
				"name":     name,
				"source":   "process",
				"unit":     "",
				"running":  true,
				"enabled":  "",
				"bin":      bin,
				"cmdline":  cmdline[:min(len(cmdline), 180)],
				"config":   "",
				"configured": false,
			})
		}
	}

	// 3) 配置文件（标准 ini/toml 隧道模式）
	for _, p := range frpConfCandidates() {
		if o, err := runBin(timeout, "sh", "-c", "[ -f "+p+" ] && echo 1 || echo 0"); err != nil || strings.TrimSpace(string(o)) != "1" {
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(filepathBase(p), ".ini"), ".toml")
		name := "default"
		if base != "frpc" {
			name = base
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		tunnels := frpTunnels(p, timeout)
		instances = append(instances, map[string]any{
			"name":       name,
			"source":     "config",
			"unit":       "",
			"running":    false,
			"enabled":    "",
			"bin":        "",
			"cmdline":    "",
			"config":     p,
			"configured": true,
			"tunnels":    tunnels,
		})
	}

	data, _ := json.Marshal(instances)
	r := e.base(cmd)
	r.Stdout = data
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Message = "ok"
	return r
}

// frpTunnels 解析 ini/toml 配置文件里的隧道段名（[xxx] 或 [[xxx]]）。
func frpTunnels(path string, timeout time.Duration) []string {
	out, err := runBin(timeout, "sh", "-c", "grep -E '^\\[{1,2}[^]]+\\]{1,2}' "+path+" 2>/dev/null")
	if err != nil {
		return nil
	}
	tunnels := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, "[]")
		line = strings.TrimSpace(line)
		if line != "" && !strings.Contains(line, "common") && !strings.Contains(line, "serverAddr") {
			tunnels = append(tunnels, line)
		}
	}
	return tunnels
}

// frpConfigGet 读取指定实例的配置文件内容（只读）。
//
// 参数：instance（默认 default）；若无配置文件则返回空并提示。
func (e *Executor) frpConfigGet(cmd *ecpv1.Command) *ecpv1.CommandResult {
	timeout := dur(cmd.GetTimeoutSec(), 15)
	inst := getString(cmd.GetParams(), "instance")
	path := e.resolveFrpConf(inst, timeout)
	if path == "" {
		r := e.base(cmd)
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
		r.Message = "未找到 frpc 配置文件（/etc/frp/frpc.ini 等），可先新增隧道生成"
		return r
	}
	out, err := runBin(timeout, "sh", "-c", "cat "+path)
	r := e.base(cmd)
	r.Stdout = out
	if err != nil {
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
		r.Message = err.Error()
		return r
	}
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Message = "path=" + path
	return r
}

// frpConfigSet 写入/修改指定实例的 frpc 配置。
//
// 参数：instance（默认 default）、content（全量覆盖）或 tunnel（追加一个隧道段）：
//   tunnel: {"name","type","local_port","remote_port","custom_domains"}
// 写 /etc/frp 需要 root：免密 sudo 可用则直接执行，否则返回人工 sudo 脚本（架构降级）。
func (e *Executor) frpConfigSet(cmd *ecpv1.Command) *ecpv1.CommandResult {
	timeout := dur(cmd.GetTimeoutSec(), 20)
	inst := getString(cmd.GetParams(), "instance")
	if inst == "" {
		inst = "default"
	}
	path := e.resolveFrpConf(inst, timeout)
	if path == "" {
		path = "/etc/frp/frpc.ini"
	}
	content := getString(cmd.GetParams(), "content")
	if content == "" {
		content = buildTunnelBlock(getString(cmd.GetParams(), "tunnel"))
		if content == "" {
			return e.fail(cmd, "缺少 content 或 tunnel 参数")
		}
		// 追加模式：读现有内容 + 新段
		if old, err := runBin(timeout, "sh", "-c", "cat "+path+" 2>/dev/null"); err == nil && len(old) > 0 {
			content = string(old) + "\n" + content
		}
	}

	if sudoOK() {
		// 免密 sudo 可用：直接写入（sudo 是系统预先授予的，非平台提权）
		script := "mkdir -p /etc/frp && cat > " + path + " <<'ECP_EOF'\n" + content + "\nECP_EOF\n"
		out, err := runBin(timeout, "sudo", "bash", "-c", script)
		r := e.base(cmd)
		r.Stdout = out
		if err != nil {
			r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
			r.Message = err.Error()
			return r
		}
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
		r.Message = "配置已写入 " + path
		return r
	}

	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
	r.PrivilegeScript = "#!/bin/bash\nset -euo pipefail\nmkdir -p /etc/frp\ncat > " + path + " <<'ECP_EOF'\n" + content + "\nECP_EOF\n"
	r.PrivilegeHint = "写入 frpc 配置需要 root，请人工 sudo 执行以下脚本"
	return r
}

// buildTunnelBlock 由隧道 JSON 生成 ini 段。
func buildTunnelBlock(tunnelJSON string) string {
	if tunnelJSON == "" {
		return ""
	}
	var t struct {
		Name          string `json:"name"`
		Type          string `json:"type"`
		LocalPort     int    `json:"local_port"`
		RemotePort    int    `json:"remote_port"`
		CustomDomains string `json:"custom_domains"`
	}
	if err := json.Unmarshal([]byte(tunnelJSON), &t); err != nil || t.Name == "" {
		return ""
	}
	if t.Type == "" {
		t.Type = "tcp"
	}
	if t.LocalPort <= 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[" + t.Name + "]\n")
	b.WriteString("type = " + t.Type + "\n")
	b.WriteString("local_ip = 127.0.0.1\n")
	b.WriteString("local_port = " + itoa(t.LocalPort) + "\n")
	if t.Type == "tcp" || t.Type == "udp" {
		if t.RemotePort > 0 {
			b.WriteString("remote_port = " + itoa(t.RemotePort) + "\n")
		}
	}
	if t.CustomDomains != "" {
		b.WriteString("custom_domains = " + t.CustomDomains + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// resolveFrpConf 按实例名定位配置文件。
func (e *Executor) resolveFrpConf(inst string, timeout time.Duration) string {
	if inst != "" && inst != "default" {
		for _, p := range []string{
			"/etc/frp/frpc-" + inst + ".ini",
			"/etc/frp/" + inst + ".ini",
			"/etc/frpc-" + inst + ".ini",
		} {
			if o, err := runBin(timeout, "sh", "-c", "[ -f "+p+" ] && echo 1 || echo 0"); err == nil && strings.TrimSpace(string(o)) == "1" {
				return p
			}
		}
		return ""
	}
	for _, p := range frpConfCandidates() {
		if o, err := runBin(timeout, "sh", "-c", "[ -f "+p+" ] && echo 1 || echo 0"); err == nil && strings.TrimSpace(string(o)) == "1" {
			return p
		}
	}
	return ""
}

// frpUp 启动指定实例的 frpc（需 root）。免密 sudo 可用则直接执行，否则降级脚本。
//
// 参数：instance（默认 default，对应 systemd 单元 frpc.service）。
func (e *Executor) frpUp(cmd *ecpv1.Command) *ecpv1.CommandResult {
	inst := getString(cmd.GetParams(), "instance")
	unit := frpUnit(inst)
	if sudoOK() {
		timeout := dur(cmd.GetTimeoutSec(), 30)
		script := "systemctl enable --now " + unit + " 2>/dev/null || (mkdir -p /etc/frp && nohup frpc -c /etc/frp/frpc.ini >/var/log/frpc.log 2>&1 &)"
		out, err := runBin(timeout, "sudo", "bash", "-c", script)
		r := e.base(cmd)
		r.Stdout = out
		if err != nil {
			r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
			r.Message = err.Error()
			return r
		}
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
		r.Message = "frpc(" + inst + ") 已启动并设置自启"
		return r
	}
	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
	r.PrivilegeScript = "#!/bin/bash\nset -euo pipefail\nsystemctl enable --now " + unit + " 2>/dev/null || (mkdir -p /etc/frp && nohup frpc -c /etc/frp/frpc.ini >/var/log/frpc.log 2>&1 &)\n"
	r.PrivilegeHint = "启动 frpc 需要 root，请人工 sudo 执行以下脚本"
	return r
}

// frpDown 停止指定实例的 frpc（需 root）。免密 sudo 可用则直接执行，否则降级脚本。
func (e *Executor) frpDown(cmd *ecpv1.Command) *ecpv1.CommandResult {
	inst := getString(cmd.GetParams(), "instance")
	unit := frpUnit(inst)
	if sudoOK() {
		timeout := dur(cmd.GetTimeoutSec(), 20)
		out, err := runBin(timeout, "sudo", "bash", "-c", "systemctl disable --now "+unit+" 2>/dev/null; pkill -x frpc 2>/dev/null || true")
		r := e.base(cmd)
		r.Stdout = out
		if err != nil {
			r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
			r.Message = err.Error()
			return r
		}
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
		r.Message = "frpc(" + inst + ") 已停止"
		return r
	}
	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
	r.PrivilegeScript = "#!/bin/bash\nset -euo pipefail\nsystemctl disable --now " + unit + " 2>/dev/null; pkill -x frpc 2>/dev/null || true\n"
	r.PrivilegeHint = "停止 frpc 需要 root，请人工 sudo 执行以下脚本"
	return r
}

// frpUnit 由实例名推导 systemd 单元名。
func frpUnit(inst string) string {
	if inst == "" || inst == "default" {
		return "frpc.service"
	}
	return "frpc-" + inst + ".service"
}

func filepathBase(p string) string {
	i := strings.LastIndex(p, "/")
	if i >= 0 {
		return p[i+1:]
	}
	return p
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
