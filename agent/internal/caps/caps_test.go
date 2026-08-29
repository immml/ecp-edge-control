package caps

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProbeDoesNotPanic(t *testing.T) {
	s := Probe()
	if s == nil {
		t.Fatal("Probe 返回 nil")
	}
	// 用户名与 UID 在任何正常情况下都应能拿到
	if s.RunAsUser == "" {
		t.Error("未取到运行用户名")
	}
	t.Logf("uid=%d user=%s docker=%v tailscale=%v net=%v",
		s.RunAsUID, s.RunAsUser, s.CanReadDocker, s.CanManageTailscale, s.CanManageNetwork)
}

// TestProbeDockerSocketContract 锁定一条关键契约：
// docker 能力必须来自"真的连得上 socket"，而不是"命令在不在 PATH"。
//
// 真机教训：Orange Pi 上 docker 二进制存在，但 orangepi 用户不在
// docker 组，用 LookPath 判断会误报为可用。
func TestProbeDockerSocketContract(t *testing.T) {
	read, write := probeDocker()

	// 连不上 socket 时，读写必须同时为 false
	if !read && write {
		t.Error("socket 不可访问却上报了可写，逻辑自相矛盾")
	}
	// 连得上 socket 时，读写应同时为 true（unix socket 权限是读写一体的）
	if read && !write {
		t.Error("socket 可访问却上报不可写，逻辑自相矛盾")
	}

	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 unix socket，跳过存在性断言")
	}
	if _, err := os.Stat(DockerSocket); os.IsNotExist(err) {
		if read {
			t.Error("socket 文件不存在却上报了可访问")
		}
	}
}

func TestCanWriteDir(t *testing.T) {
	dir := t.TempDir()
	if !canWriteDir(dir) {
		t.Error("临时目录应当可写")
	}
	if canWriteDir(filepath.Join(dir, "no-such-dir")) {
		t.Error("不存在的目录不该判定为可写")
	}
	if canWriteDir("") {
		t.Error("空路径不该判定为可写")
	}
}

func TestCanRead(t *testing.T) {
	f := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !canRead(f) {
		t.Error("刚写的文件应当可读")
	}
	if canRead(filepath.Join(t.TempDir(), "missing.txt")) {
		t.Error("不存在的文件不该判定为可读")
	}
}
