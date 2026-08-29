// Package collector 采集节点系统指标。
//
// 这是 T4-B 的"采集"部分：周期性读取 CPU/内存/磁盘/网络/负载/温度/容器，
// 产出与控制面 proto 完全对齐的 ecpv1.Telemetry，既回填心跳也落本地缓存。
//
// 设计要点：
//   - 网络字节是累计值（net.IOCounters），由控制面算差速，Agent 只负责如实上报。
//   - 温度优先取传感器，读不到再回退 /sys/class/thermal（Orange Pi 常用）。
//   - 容器数仅在具备 docker 读能力时统计，否则为 0，绝不硬依赖 docker。
package collector

import (
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/agent/internal/caps"
)

// Collector 持有采集所需的本地状态。
type Collector struct {
	canReadDocker bool
}

// New 构造采集器。
func New(c *caps.Set) *Collector {
	return &Collector{canReadDocker: c.CanReadDocker}
}

// Collect 采集一次完整的遥测快照。
//
// 任何单项采集失败都不影响其余字段——边缘节点上某些指标可能取不到，
// 宁可少报一项也不能让整个采集挂掉。
func (c *Collector) Collect() *ecpv1.Telemetry {
	t := &ecpv1.Telemetry{}

	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		t.CpuPercent = pct[0]
	}

	if vm, err := mem.VirtualMemory(); err == nil {
		t.MemTotalBytes = vm.Total
		t.MemUsedBytes = vm.Used
	}

	if du, err := disk.Usage("/"); err == nil {
		t.DiskTotalBytes = du.Total
		t.DiskUsedBytes = du.Used
	}

	if io, err := net.IOCounters(false); err == nil && len(io) > 0 {
		t.NetRxBytes = io[0].BytesRecv
		t.NetTxBytes = io[0].BytesSent
	}

	if avg, err := load.Avg(); err == nil {
		t.Load1 = avg.Load1
		t.Load5 = avg.Load5
	}

	t.TemperatureCelsius = readTemperature()

	if c.canReadDocker {
		run, total := countContainers()
		t.ContainerRunning = run
		t.ContainerTotal = total
	}

	return t
}

// readTemperature 读取 CPU/SoC 温度（摄氏度）。优先 thermal_zone，回退 sysfs。
func readTemperature() float64 {
	for _, path := range []string{
		"/sys/class/thermal/thermal_zone0/temp",
		"/sys/class/thermal/thermal_zone1/temp",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(data))
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			if v > 1000 { // 毫摄氏度
				return v / 1000
			}
			return v
		}
	}
	return 0
}

// countContainers 统计容器总数与运行中数量。仅在能读 docker 时有效。
func countContainers() (running, total uint32) {
	out, err := exec.Command("docker", "ps", "-a", "--format", "{{.Status}}").Output()
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		total++
		if strings.HasPrefix(strings.ToLower(line), "up") {
			running++
		}
	}
	return running, total
}
