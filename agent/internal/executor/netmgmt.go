// 网络管理：WiFi 扫描/连接、以太网编辑、信道、ping、测速、设备信息
// （DHCP/手动/PPPoE）、虚拟 MAC。底层基于系统自带 NetworkManager（nmcli）
// 与标准工具（iw/ping），需要写操作时走 sudoOK 提权（NEEDS_PRIVILEGE 降级）。
//
// 指令语义（proto NET_GET/NET_SET + params.action）：
//   NET_GET:
//     wifi_scan   扫描可用 WiFi（SSID/信号/安全/信道）
//     wifi_status 当前 WiFi 连接与信号、信道
//     channel     网卡当前信道/频率
//     devices     设备与连接总览
//     ip          各网卡 IP/掩码/网关/DNS
//     ethernet    活跃以太网连接详情（IP 配置方式）
//     ping        连通性测试（params.host/count）
//     speedtest   测速（需 /opt/ecp-agent/bin/speedtest-go 或 PATH 内 speedtest-go）
//   NET_SET:
//     wifi_connect     连接 WiFi（params.ssid/password）
//     ethernet_edit    新建/修改有线连接（params.conn/method/address/gateway/dns）
//     ip_mode          切换 DHCP/manual/PPPoE（params.conn/mode/address/gateway/dns/
//                      username/password for pppoe）
//     virtual_mac      生成虚拟 MAC+IP（保持当前子网掩码）并建连接（params.iface/ssid）
package executor

import (
	"os"
	"path/filepath"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"regexp"
	"strings"
	"time"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// speedtestBin 测速二进制候选路径（pip 安装的独立二进制）。
var speedtestBin = []string{"/opt/ecp-agent/bin/speedtest-go", "/usr/local/bin/speedtest-go", "/usr/bin/speedtest-go"}

// netGet 只读网络信息查询。
func (e *Executor) netGet(cmd *ecpv1.Command) *ecpv1.CommandResult {
	action := getString(cmd.GetParams(), "action")
	timeout := dur(cmd.GetTimeoutSec(), 25)
	switch action {
	case "wifi_scan":
		return e.netRun(cmd, timeout, "nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY,CHAN", "dev", "wifi", "list")
	case "wifi_status":
		return e.netRun(cmd, timeout, "nmcli", "-t", "-f", "SSID,SIGNAL,CHAN,DEVICE", "dev", "wifi", "show")
	case "channel":
		iface := getString(cmd.GetParams(), "iface")
		if iface == "" {
			iface = defaultWiFiIface()
		}
		out, _ := runBin(timeout, "iw", "dev", iface, "info")
		return e.textResult(cmd, string(out))
	case "devices":
		out, _ := runBin(timeout, "nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device", "status")
		return e.textResult(cmd, string(out))
	case "ip":
		return e.netRun(cmd, timeout, "ip", "-4", "addr", "show")
	case "ethernet":
		conn := getString(cmd.GetParams(), "conn")
		if conn == "" {
			conn = nmActiveConn("")
		}
		if conn == "" {
			return e.textResult(cmd, "未找到活跃连接")
		}
		return e.netRun(cmd, timeout, "nmcli", "-t", "-f",
			"NAME,DEVICE,TYPE,IP4.ADDRESS,IP4.GATEWAY,IP4.DNS,IP4.METHOD", "connection", "show", conn)
	case "ping":
		host := getString(cmd.GetParams(), "host")
		if host == "" {
			return e.fail(cmd, "缺少 host 参数")
		}
		cnt := getString(cmd.GetParams(), "count")
		if cnt == "" {
			cnt = "4"
		}
		out, err := runBin(timeout, "ping", "-c", cnt, "-W", "2", host)
		return e.textResult(cmd, string(out)+errString(err))
	case "speedtest":
		bin := findSpeedtestBin()
		if bin == "" {
			return e.fail(cmd, "未安装测速工具：将 speedtest-go 放到 agent 同目录或 /opt/ecp-agent/bin/ 下")
		}
		out, err := runBin(timeout, bin, "--json", "--saving-mode")
		if err != nil {
			return e.fail(cmd, "测速失败: "+errString(err))
		}
		type srv struct {
			Name    string  `json:"name"`
			Latency float64 `json:"latency"`
			Jitter  float64 `json:"jitter"`
			DlSpeed float64 `json:"dl_speed"`
			UlSpeed float64 `json:"ul_speed"`
		}
		var res struct {
			Servers []srv `json:"servers"`
		}
		if err := json.Unmarshal(stripJSONP(out), &res); err != nil || len(res.Servers) == 0 {
			return e.textResult(cmd, "测速未返回有效数据:\n"+string(out))
		}
		v := res.Servers[0]
		lat, jit := v.Latency, v.Jitter
		if lat > 1e6 {
			lat /= 1e6 // ns → ms
		}
		if jit > 1e6 {
			jit /= 1e6
		}
		return e.textResult(cmd, fmt.Sprintf("节点 %s\n延迟 %.1f ms (jitter %.1f)\n下载 %.2f Mbps\n上传 %.2f Mbps",
			v.Name, lat, jit, v.DlSpeed*8/1e6, v.UlSpeed*8/1e6))
	default:
		return e.fail(cmd, "未知 net_get action: "+action)
	}
}

// netSet 网络配置修改（提权敏感）。
func (e *Executor) netSet(cmd *ecpv1.Command) *ecpv1.CommandResult {
	action := getString(cmd.GetParams(), "action")
	timeout := dur(cmd.GetTimeoutSec(), 30)
	switch action {
	case "wifi_connect":
		ssid := getString(cmd.GetParams(), "ssid")
		pw := getString(cmd.GetParams(), "password")
		if ssid == "" {
			return e.fail(cmd, "缺少 ssid 参数")
		}
		args := []string{"dev", "wifi", "connect", ssid}
		if pw != "" {
			args = append(args, "password", pw)
		}
		return e.netRunPriv(cmd, timeout, "nmcli", args...)
	case "ethernet_edit":
		conn := getString(cmd.GetParams(), "conn")
		method := getString(cmd.GetParams(), "method")
		// 无 conn → 新建（需要一个接口名）
		if conn == "" {
			iface := getString(cmd.GetParams(), "iface")
			if iface == "" {
				return e.fail(cmd, "新建连接需提供 iface")
			}
			conn = "ecp-eth-" + iface
			args := []string{"connection", "add", "type", "ethernet", "con-name", conn, "ifname", iface}
			if r := e.netRunPriv(cmd, timeout, "nmcli", args...); r.GetStatus() != ecpv1.ResultStatus_RESULT_STATUS_OK {
				return r
			}
		}
		args := []string{"connection", "modify", conn}
		switch method {
		case "manual", "":
			if addr := getString(cmd.GetParams(), "address"); addr != "" {
				args = append(args, "ipv4.addresses", addr)
			}
			if gw := getString(cmd.GetParams(), "gateway"); gw != "" {
				args = append(args, "ipv4.gateway", gw)
			}
			if dns := getString(cmd.GetParams(), "dns"); dns != "" {
				args = append(args, "ipv4.dns", dns)
			}
			args = append(args, "ipv4.method", "manual")
		default: // dhcp / auto
			args = append(args, "ipv4.method", method)
		}
		if r := e.netRunPriv(cmd, timeout, "nmcli", args...); r.GetStatus() != ecpv1.ResultStatus_RESULT_STATUS_OK {
			return r
		}
		return e.netRunPriv(cmd, timeout, "nmcli", "connection", "up", conn)
	case "ip_mode":
		conn := getString(cmd.GetParams(), "conn")
		if conn == "" {
			conn = nmActiveConn("")
		}
		if conn == "" {
			return e.fail(cmd, "未找到活跃连接，请指定 conn")
		}
		mode := getString(cmd.GetParams(), "mode")
		switch mode {
		case "dhcp", "auto":
			if r := e.netRunPriv(cmd, timeout, "nmcli", "connection", "modify", conn, "ipv4.method", "auto"); r.GetStatus() != ecpv1.ResultStatus_RESULT_STATUS_OK {
				return r
			}
			return e.netRunPriv(cmd, timeout, "nmcli", "connection", "up", conn)
		case "manual":
			addr := getString(cmd.GetParams(), "address")
			if addr == "" {
				return e.fail(cmd, "manual 模式需提供 address（如 192.168.1.100/24）")
			}
			args := []string{"connection", "modify", conn, "ipv4.method", "manual", "ipv4.addresses", addr}
			if gw := getString(cmd.GetParams(), "gateway"); gw != "" {
				args = append(args, "ipv4.gateway", gw)
			}
			if r := e.netRunPriv(cmd, timeout, "nmcli", args...); r.GetStatus() != ecpv1.ResultStatus_RESULT_STATUS_OK {
				return r
			}
			return e.netRunPriv(cmd, timeout, "nmcli", "connection", "up", conn)
		case "pppoe":
			iface := getString(cmd.GetParams(), "iface")
			user := getString(cmd.GetParams(), "username")
			pw := getString(cmd.GetParams(), "password")
			if iface == "" || user == "" || pw == "" {
				return e.fail(cmd, "pppoe 模式需提供 iface/username/password")
			}
			pc := "ecp-pppoe-" + iface
			args := []string{"connection", "add", "type", "pppoe", "con-name", pc,
				"ifname", iface, "username", user, "password", pw, "ipv4.method", "auto"}
			if r := e.netRunPriv(cmd, timeout, "nmcli", args...); r.GetStatus() != ecpv1.ResultStatus_RESULT_STATUS_OK {
				return r
			}
			return e.netRunPriv(cmd, timeout, "nmcli", "connection", "up", pc)
		default:
			return e.fail(cmd, "未知 ip_mode: "+mode+"（dhcp/manual/pppoe）")
		}
	case "virtual_mac":
		return e.virtualMac(cmd, timeout)
	default:
		return e.fail(cmd, "未知 net_set action: "+action)
	}
}

// virtualMac 生成虚拟 MAC（unicast + locally administered）+ 同子网虚拟 IP，
// 建立新 WiFi 连接 profile（cloned-mac-address + 静态 IP），保持当前掩码不变。
func (e *Executor) virtualMac(cmd *ecpv1.Command, timeout time.Duration) *ecpv1.CommandResult {
	iface := getString(cmd.GetParams(), "iface")
	ssid := getString(cmd.GetParams(), "ssid")
	if iface == "" {
		iface = defaultWiFiIface()
	}
	if ssid == "" {
		return e.fail(cmd, "虚拟 MAC 连接需要 ssid（应用到该 WiFi）")
	}

	// 当前子网（ip -4 addr）→ 掩码长
	_, cidr := currentSubnet(iface)
	if cidr <= 0 {
		return e.fail(cmd, "无法获取 "+iface+" 当前子网")
	}
	// 网关/DNS 从活跃连接继承
	activeConn := nmActiveConn(iface)
	gw, _ := nmConnValue(activeConn, "IP4.GATEWAY")
	dns, _ := nmConnValue(activeConn, "IP4.DNS")
	if dns == "" {
		if g2, _ := nmConnValue(activeConn, "IP4.DNS[1]"); g2 != "" {
			dns = g2
		}
	}

	mac := genRandMAC()
	ip := genRandHostIP(iface, cidr)
	name := "ecp-vmac-" + strings.ReplaceAll(mac, ":", "")

	args := []string{"connection", "add", "type", "wifi", "con-name", name, "ifname", iface,
		"ssid", ssid,
		"802-11-wireless.cloned-mac-address", mac,
		"ipv4.method", "manual", "ipv4.addresses", ip + "/" + itoa(cidr)}
	if gw != "" {
		args = append(args, "ipv4.gateway", gw)
	}
	if dns != "" {
		args = append(args, "ipv4.dns", dns)
	}
	if r := e.netRunPriv(cmd, timeout, "nmcli", args...); r.GetStatus() != ecpv1.ResultStatus_RESULT_STATUS_OK {
		return r
	}
	if r := e.netRunPriv(cmd, timeout, "nmcli", "connection", "up", name); r.GetStatus() != ecpv1.ResultStatus_RESULT_STATUS_OK {
		return r
	}
	return e.textResult(cmd, fmt.Sprintf("虚拟连接已启用：%s\n  虚拟 MAC: %s\n  虚拟 IP : %s/%d（沿用当前子网掩码）\n  网关    : %s\n  连接名  : %s",
		ssid, mac, ip, cidr, gw, name))
}

// --- 辅助 ---------------------------------------------------------------

// netRun 只读执行（普通用户即可），失败返回文本。
func (e *Executor) netRun(cmd *ecpv1.Command, timeout time.Duration, name string, args ...string) *ecpv1.CommandResult {
	out, err := runBin(timeout, name, args...)
	if err != nil {
		return e.textResult(cmd, errString(err))
	}
	return e.textResult(cmd, string(out))
}

// netRunPriv 提权执行：免密 sudo 直行，否则 NEEDS_PRIVILEGE 返回人工脚本。
func (e *Executor) netRunPriv(cmd *ecpv1.Command, timeout time.Duration, name string, args ...string) *ecpv1.CommandResult {
	if sudoOK() {
		priv := append([]string{"-n"}, append([]string{name}, args...)...)
		out, err := runBin(timeout, "sudo", priv...)
		if err != nil {
			return e.fail(cmd, "命令失败: "+errString(err))
		}
		return e.textResult(cmd, string(out))
	}
	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
	r.PrivilegeScript = "sudo nmcli " + strings.Join(append([]string{name}, args...), " ")
	r.Message = "该操作需要提权，请以 root 执行以下命令并确认后重试"
	return r
}

// textResult 构造纯文本成功结果。
func (e *Executor) textResult(cmd *ecpv1.Command, text string) *ecpv1.CommandResult {
	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_OK
	r.Stdout = []byte(text)
	return r
}

// errString 提取可读错误信息。
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// stripJSONP 清除 speedtest-go 输出前置日志（取其最后一个 { 开始的 JSON 对象）。
func stripJSONP(b []byte) []byte {
	s := string(b)
	if i := strings.Index(s, "{"); i >= 0 {
		return []byte(s[i:])
	}
	return b
}

// findSpeedtestBin 在候选路径中找测速二进制（优先 agent 同级，随分发一起走）。
func findSpeedtestBin() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "speedtest-go")
		if pathExists(p) {
			return p
		}
	}
	for _, p := range speedtestBin {
		if pathExists(p) {
			return p
		}
	}
	return ""
}

// pathExists 判断文件存在且可执行（近似）。
func pathExists(p string) bool {
	_, err := runBin(2*time.Second, "test", "-x", p)
	return err == nil
}

// defaultWiFiIface 从 nmcli device status 找第一个 wifi 设备。
func defaultWiFiIface() string {
	out, _ := runBin(3*time.Second, "nmcli", "-t", "-f", "DEVICE,TYPE", "device", "status")
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) >= 2 && parts[1] == "wifi" {
			return parts[0]
		}
	}
	return "wlan0"
}

// nmActiveConn 取指定接口（或首个活跃）的连接名。
func nmActiveConn(iface string) string {
	args := []string{"-t", "-f", "NAME,DEVICE", "connection", "show", "--active"}
	out, _ := runBin(3*time.Second, "nmcli", args...)
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) < 2 {
			continue
		}
		if iface == "" || parts[1] == iface {
			return parts[0]
		}
	}
	return ""
}

// nmConnValue 取连接某个属性。
func nmConnValue(conn, field string) (string, bool) {
	if conn == "" {
		return "", false
	}
	out, _ := runBin(3*time.Second, "nmcli", "-t", "-f", "NAME,"+field, "connection", "show", conn)
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, ":"); i > 0 && line[:i] == conn {
			return line[i+1:], true
		}
	}
	return "", false
}

// currentSubnet 取接口当前 IPv4 前缀长度（>0 表示拿到）。
func currentSubnet(iface string) (string, int) {
	out, _ := runBin(3*time.Second, "ip", "-4", "addr", "show", iface)
	re := regexp.MustCompile(`inet\s+(\d+\.\d+\.\d+\.\d+)/(\d+)`)
	m := re.FindStringSubmatch(string(out))
	if len(m) < 3 {
		return "", 0
	}
	parts := strings.Split(m[1], ".")
	if len(parts) != 4 {
		return "", 0
	}
	prefix := atoi(m[2])
	return parts[0] + "." + parts[1] + "." + parts[2] + ".", prefix
}

// genRandMAC 生成 locally administered 随机 MAC（xx:xx:xx:xx:xx:xx）。
func genRandMAC() string {
	b := make([]byte, 6)
	for i := range b {
		b[i] = byte(rand.Intn(256))
	}
	b[0] = b[0]&0xFE | 0x02 // unicast + locally administered
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
}

// genRandHostIP 同网段随机主机位（避开 0/1/255 与网关）。
func genRandHostIP(iface string, prefix int) string {
	base, _ := currentSubnet(iface)
	maxHost := 1 << (32 - prefix)
	host := 2 + rand.Intn(maxHost-3)
	if host > 254 {
		host = 254
	}
	if host == 0 || host == 1 || host == 255 {
		host = 100
	}
	return fmt.Sprintf("%s%d", base, host)
}

// atoi 简易字符串转 int。
func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// 检查编译期依赖（net 包引用，避免未使用告警）。
var _ = net.ParseIP