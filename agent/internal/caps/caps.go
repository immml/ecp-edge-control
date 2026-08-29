// Package caps 探测 Agent 在当前权限下究竟能做什么。
//
// 这是"Agent 默认非 root"这条架构约束的落地处。核心原则：
//
//	不看命令在不在，只看操作能不能成。
//
// 一个典型反例：docker 二进制存在于 PATH，但执行者不在 docker 组，
// 于是 `docker ps` 必然失败。如果用 exec.LookPath 判断，就会误报为
// "支持容器管理"，用户点下去才报错——这是最糟糕的体验。
package caps

import (
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"time"
)

// Set 是能力探测结果，与 proto 的 CapabilityReport 一一对应。
type Set struct {
	CanReadSystemStats bool
	CanTerminal        bool
	CanManageFiles     bool
	CanReadDocker      bool
	CanWriteDocker     bool
	CanManageTailscale bool
	CanManageNetwork   bool
	CanManageSystemd   bool
	CanSelfUpgrade     bool
	CanReadNetConfig   bool

	RunAsUID     int
	RunAsUser    string
	MissingTools []string
}

// DockerSocket 是 Docker 守护进程的默认套接字路径。
const DockerSocket = "/var/run/docker.sock"

// Probe 执行完整的能力探测。
//
// 所有探测都必须有超时——Agent 启动不能因为某个命令卡住而挂起。
func Probe() *Set {
	s := &Set{}

	if u, err := user.Current(); err == nil {
		s.RunAsUser = u.Username
		if uid, err := strconv.Atoi(u.Uid); err == nil {
			s.RunAsUID = uid
		}
	}

	s.CanReadSystemStats = canRead("/proc/stat") && canRead("/proc/meminfo")
	s.CanTerminal = true    // pty 走 creack/pty，无需特权
	s.CanManageFiles = true // 受目标路径权限约束，运行时另行判定
	s.CanReadNetConfig = canRead("/etc/resolv.conf") || hasCommand("ip")

	// Docker：必须真连得上套接字才算数
	s.CanReadDocker, s.CanWriteDocker = probeDocker()

	// Tailscale：能免密读到状态才算"可纳管"
	s.CanManageTailscale = canRunTailscaleStatus()

	// 网络控制：iptables -L 无需特权即可读，写需要 root
	s.CanManageNetwork = canExec("iptables", "-L", "-n")

	// systemd：用户级实例无需 root
	s.CanManageSystemd = canExec("systemctl", "--user", "status")

	// 自升级：对自身二进制所在目录可写
	s.CanSelfUpgrade = canWriteDir(executableDir())

	for _, tool := range []string{"tailscale", "docker", "ip", "iptables"} {
		if !hasCommand(tool) {
			s.MissingTools = append(s.MissingTools, tool)
		}
	}

	return s
}

// probeDocker 判断 Docker 套接字是否可访问。
//
// 能连上 unix socket 即代表拥有完整的 Docker API 权限（含写操作），
// 所以读写能力在这里是一致的。
func probeDocker() (read, write bool) {
	conn, err := net.DialTimeout("unix", DockerSocket, 2*time.Second)
	if err != nil {
		return false, false
	}
	_ = conn.Close()
	return true, true
}

// canRunTailscaleStatus 判断能否免密执行 tailscale status。
//
// 注意：tailscale 二进制存在不代表能免密控制它。未加入 docker 组式的
// 权限问题在这里同样适用——tailscaled 的 socket 默认只对 root 开放。
func canRunTailscaleStatus() bool {
	if !hasCommand("tailscale") {
		return false
	}
	return canExec("tailscale", "status", "--json")
}

// canExec 尝试执行命令并等待其成功退出。
func canExec(name string, args ...string) bool {
	ctx := exec.Command(name, args...)
	if err := ctx.Start(); err != nil {
		return false
	}
	done := make(chan error, 1)
	go func() { done <- ctx.Wait() }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(3 * time.Second):
		_ = ctx.Process.Kill()
		return false
	}
}

// hasCommand 判断命令是否在 PATH 中。
func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// canRead 判断文件是否可读。
func canRead(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// canWriteDir 判断目录是否可写，用临时文件实测。
func canWriteDir(dir string) bool {
	if dir == "" {
		return false
	}
	tmp := filepath.Join(dir, ".ecp-write-test")
	f, err := os.Create(tmp)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(tmp)
	return true
}

// executableDir 返回 Agent 自身所在目录。
func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}
