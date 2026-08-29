package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// dockerList 列出容器。读操作，需 CanReadDocker（能连上 docker.sock）。
//
// 输出结构化 JSON 数组供控制台渲染（每行一个 docker ps json 对象，转精简字段）。
func (e *Executor) dockerList(cmd *ecpv1.Command) *ecpv1.CommandResult {
	if !e.caps.CanReadDocker {
		return e.needsDockerPriv(cmd, "读取容器列表需要 docker 套接字访问权限（加入 docker 组）")
	}
	timeout := dur(cmd.GetTimeoutSec(), 30)
	out, err := runDocker(timeout, "ps", "-a", "--format", "json")
	r := e.base(cmd)
	if err != nil {
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
		r.Message = err.Error()
		return r
	}
	type rawItem struct {
		Names  string
		Image  string
		Status string
		State  string
		Ports  string
		Labels string
	}
	items := make([]map[string]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw rawItem
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue // 跳过非 JSON 行（docker 版本差异），不阻塞整表
		}
		items = append(items, map[string]string{
			"name":    raw.Names,
			"image":   raw.Image,
			"status":  raw.Status,
			"state":   raw.State,
			"ports":   raw.Ports,
			"labels":  raw.Labels,
			"managed": managedLabel(raw.Labels),
		})
	}
	if data, err := json.Marshal(items); err == nil {
		r.Stdout = data
	}
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Message = "ok"
	return r
}

// managedLabel 从 docker 标签串（key=value,key=value）中提取是否带 ecp.managed=true。
func managedLabel(labels string) string {
	for _, kv := range strings.Split(labels, ",") {
		if strings.TrimSpace(kv) == "ecp.managed=true" {
			return "true"
		}
	}
	return ""
}

// dockerAction 对容器执行 start / stop / restart。
//
// 写操作，需 CanWriteDocker（在 docker 组）。隔离红线：只允许操作带
// ecp.managed=true 标签的容器，禁止批量或误操作节点上的业务容器。
func (e *Executor) dockerAction(cmd *ecpv1.Command) *ecpv1.CommandResult {
	if !e.caps.CanWriteDocker {
		return e.needsDockerPriv(cmd, "该操作需要 docker 写权限（加入 docker 组）")
	}
	action := strings.ToLower(getString(cmd.GetParams(), "action"))
	name := getString(cmd.GetParams(), "container")
	if name == "" {
		return e.fail(cmd, "缺少 container 参数")
	}
	if action != "start" && action != "stop" && action != "restart" {
		return e.fail(cmd, "不支持的 action: "+action+"（仅支持 start/stop/restart）")
	}

	// 隔离校验：只允许带 ecp.managed=true 标签的容器
	if !e.containerManaged(name) {
		r := e.base(cmd)
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_REJECTED
		r.Message = "拒绝：容器 " + name + " 未打 ecp.managed=true 标签，按隔离红线不对其执行写操作"
		return r
	}

	timeout := dur(cmd.GetTimeoutSec(), 60)
	out, err := runDocker(timeout, action, name)
	r := e.base(cmd)
	r.Stdout = out
	if err != nil {
		r.Status = ecpv1.ResultStatus_RESULT_STATUS_FAILED
		r.Message = err.Error()
		return r
	}
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Message = "容器 " + name + " " + action + " 成功"
	return r
}

// dockerLogs 拉取容器日志。读操作，需 CanReadDocker。
func (e *Executor) dockerLogs(cmd *ecpv1.Command) *ecpv1.CommandResult {
	if !e.caps.CanReadDocker {
		return e.needsDockerPriv(cmd, "读取容器日志需要 docker 套接字访问权限（加入 docker 组）")
	}
	name := getString(cmd.GetParams(), "container")
	if name == "" {
		return e.fail(cmd, "缺少 container 参数")
	}
	lines := getInt(cmd.GetParams(), "lines")
	if lines <= 0 {
		lines = 100
	}
	timeout := dur(cmd.GetTimeoutSec(), 30)
	out, err := runDocker(timeout, "logs", "--tail", itoa(lines), name)
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

// containerManaged 检查容器是否带 ecp.managed=true 标签。
func (e *Executor) containerManaged(name string) bool {
	timeout := 10 * time.Second
	label, err := runDocker(timeout, "inspect", "-f", "{{index .Config.Labels \"ecp.managed\"}}", name)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(label)) == "true"
}

// needsDockerPriv 返回一条"权限不足"的结果。
//
// 注意：docker 能力缺失是组归属问题（不在 docker 组），sudo 单次提权无法解决，
// 因此用 REJECTED 而非 NEEDS_PRIVILEGE（后者留给"只需 sudo 一次"的场景）。
func (e *Executor) needsDockerPriv(cmd *ecpv1.Command, hint string) *ecpv1.CommandResult {
	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_REJECTED
	r.PrivilegeHint = hint
	r.Message = "Docker 能力不足"
	return r
}

// runDocker 执行 docker CLI，带超时。返回 stdout 与 error。
func runDocker(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out bytes.Buffer
	//nolint:gosec // 受控指令：参数来自控制面授权后的下发，按设计执行本地 docker。
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.Bytes(), err
	}
	return out.Bytes(), err
}

// dur 把秒数转成超时，非法值回退到 def。
func dur(sec int32, def int) time.Duration {
	if sec <= 0 {
		return time.Duration(def) * time.Second
	}
	return time.Duration(sec) * time.Second
}

// itoa 是小整数转字符串的快捷方式。
func itoa(n int) string {
	return strconv.Itoa(n)
}
