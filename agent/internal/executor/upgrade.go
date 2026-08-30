package executor

import (
	"os"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// agentUpgrade 在线升级 Agent 自身（OTA）。
//
// 参数：
//   - url：新二进制下载地址（控制面静态端点，HTTPS）
//   - sha256：新二进制 SHA256（下载后强制校验，防篡改）
//
// 流程：curl 下载到 /tmp → sha256 校验 → 备份当前二进制 → 原子替换
// （os.Executable() 定位自身路径）→ systemctl restart 拉起新版本。
// 注意：restart 会杀掉当前进程，控制面该指令的结果可能收不到（预期行为，
// 前端按"升级中，稍后刷新"处理）。提权：免密 sudo 直接执行，否则降级脚本。
func (e *Executor) agentUpgrade(cmd *ecpv1.Command) *ecpv1.CommandResult {
	timeout := dur(cmd.GetTimeoutSec(), 120)
	url := getString(cmd.GetParams(), "url")
	sha := getString(cmd.GetParams(), "sha256")
	if url == "" || sha == "" {
		return e.fail(cmd, "缺少 url / sha256 参数")
	}

	self, err := os.Executable()
	if err != nil {
		return e.fail(cmd, "无法定位自身二进制: "+err.Error())
	}

	script := `set -euo pipefail
cd /tmp
echo "下载新版本: ` + url + `"
curl -fsSLk -o ecp-agent.new '` + url + `'
[ -s ecp-agent.new ] || { echo "下载失败（文件为空）"; exit 1; }
echo '` + sha + `  ecp-agent.new' | sha256sum -c - || { echo "SHA256 校验失败，已中止"; exit 1; }
chmod +x ecp-agent.new
cp -f '` + self + `' '` + self + `.bak' && echo "已备份旧版本到 ecp-agent.bak"
mv -f ecp-agent.new '` + self + `'
echo "替换完成，重启 Agent..."
systemctl restart ecp-agent
`

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
		r.Message = "升级指令已执行（Agent 重启中）"
		return r
	}

	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
	r.PrivilegeScript = "#!/bin/bash\nset -euo pipefail\n" + script + "\n"
	r.PrivilegeHint = "Agent 升级需要 root 权限，请人工 sudo 执行以下脚本"
	return r
}
