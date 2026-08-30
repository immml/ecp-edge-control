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
	"bytes"
	"context"
	"os"
	"os/exec"
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
		// 先触发 NM 主动扫描。注意：NetworkManager 默认禁止普通用户 rescan
		// （org.freedesktop.NetworkManager.wifi.scan: not authorized），
		// 有免密 sudo 时用 sudo 触发；否则尝试普通权限，失败不阻塞。
		iface := getString(cmd.GetParams(), "iface")
		if iface == "" {
			iface = defaultWiFiIface()
		}
		if sudoOK() {
			_, _ = runBin(10*time.Second, "sudo", "-n", "nmcli", "dev", "wifi", "rescan", "ifname", iface)
		} else {
			_, _ = runBin(10*time.Second, "nmcli", "dev", "wifi", "rescan", "ifname", iface)
		}
		time.Sleep(3 * time.Second) // rescan 为异步，等 NM 完成一轮扫描
		out, err := runBin(timeout, "nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY,CHAN", "dev", "wifi", "list", "ifname", iface)
		text := string(out)
		if err != nil || strings.TrimSpace(text) == "" {
			// nmcli 无结果 → iw 直扫兜底（嵌入式网卡常不进 NM 扫描列表）
			if sudoOK() {
				if iwOut, iwErr := runBin(20*time.Second, "sudo", "-n", "iw", "dev", iface, "scan"); iwErr == nil {
					if parsed := parseIwScan(iwOut); parsed != "" {
						return e.textResult(cmd, parsed)
					}
				}
			}
			return e.textResult(cmd, "未发现 WiFi 网络（已尝试 nmcli rescan + iw scan）\n"+errString(err)+"\n"+strings.TrimSpace(text))
		}
		return e.textResult(cmd, text)
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
		// 完整模式（不用 --saving-mode：省流量模式上传包小，结果偏差大，用户实测上传偏低）
		out, err := runBin(timeout, bin, "--json")
		if err != nil {
			return e.fail(cmd, "测速失败: "+errString(err))
		}
		type srv struct {
			Name    string  `json:"name"`
			Country string  `json:"country"`
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
		// 选延迟最低的服务器（speedtest-go 默认可能给多个，首个不一定最优）
		v := res.Servers[0]
		for _, s := range res.Servers {
			if s.Latency > 0 && s.Latency < v.Latency && s.DlSpeed > 0 {
				v = s
			}
		}
		lat, jit := v.Latency, v.Jitter
		if lat > 1e6 {
			lat /= 1e6 // ns → ms
		}
		if jit > 1e6 {
			jit /= 1e6
		}
		region := v.Country
		result := fmt.Sprintf("节点 %s (%s)\n延迟 %.1f ms (jitter %.1f)\n下载 %.2f Mbps\n上传 %.2f Mbps\n服务器数量 %d",
			v.Name, region, lat, jit, v.DlSpeed*8/1e6, v.UlSpeed*8/1e6, len(res.Servers))
		return e.textResult(cmd, result)
	case "ip_quality":
		// IP 质量体检：复用公开脚本 xykt/IPQuality（-j JSON，-4 仅 IPv4，-n 跳过依赖安装）。
		// 首次运行自动下载脚本到 agent 同目录，需节点能访问 GitHub。
		// 完整检测含 AI/邮局/DNSBL 多项，耗时 2-6 分钟，超时放宽。
		const ipqURL = "https://raw.githubusercontent.com/xykt/IPQuality/main/ip.sh"
		ipqPath := filepath.Join(agentDir(), "ipquality.sh")
		if !pathExists(ipqPath) {
			dl, derr := runBin(60*time.Second, "curl", "-fsSL", "--max-time", "50", "-o", ipqPath, ipqURL)
			if derr != nil {
				return e.fail(cmd, "下载 IPQuality 脚本失败（节点需可访问 GitHub）: "+errString(derr)+"\n"+string(dl))
			}
			runBin(5*time.Second, "chmod", "+x", ipqPath)
		}
		out, err := runBin(360*time.Second, "bash", ipqPath, "-j", "-4", "-n")
		if err != nil {
			return e.textResult(cmd, "IP 体检执行异常（需 dig/jq/curl/nc/bc 依赖）:\n"+errString(err)+"\n"+strings.TrimSpace(stripANSI(string(out))))
		}
		parsed := parseIPQualityJSON(stripJSONP([]byte(stripANSI(string(out)))))
		if parsed == "" {
			return e.textResult(cmd, "IP 体检完成（未解析出结构化数据）:\n"+stripANSI(string(out)))
		}
		return e.textResult(cmd, parsed)
	default:
		return e.fail(cmd, "未知 net_get action: "+action)
	}
}

// agentDir 返回当前 agent 可执行文件所在目录。
func agentDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

// parseIPQualityJSON 把 IPQuality 的 -j JSON 输出压缩成易读文本。
func parseIPQualityJSON(b []byte) string {
	var d struct {
		Info struct {
			ASN          string `json:"ASN"`
			Organization string `json:"Organization"`
			Type         string `json:"Type"`
		} `json:"Info"`
		Score struct {
			Scamalytics  string `json:"SCAMALYTICS"`
			IP2LOCATION  string `json:"IP2LOCATION"`
			AbuseIPDB    string `json:"AbuseIPDB"`
			ScamalyticsV string `json:"SCAMALYTICS"`
		} `json:"Score"`
		Media struct {
			TikTok     struct{ Status string `json:"Status"` } `json:"TikTok"`
			Netflix    struct{ Status string `json:"Status"` } `json:"Netflix"`
			DisneyPlus struct{ Status string `json:"Status"` } `json:"DisneyPlus"`
			YouTube    struct{ Status string `json:"Status"` } `json:"Youtube"`
			ChatGPT    struct{ Status string `json:"Status"` } `json:"ChatGPT"`
		} `json:"Media"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return ""
	}
	media := ""
	for _, kv := range [][2]string{
		{"TikTok", d.Media.TikTok.Status},
		{"Netflix", d.Media.Netflix.Status},
		{"Disney+", d.Media.DisneyPlus.Status},
		{"YouTube", d.Media.YouTube.Status},
		{"ChatGPT", d.Media.ChatGPT.Status},
	} {
		if kv[1] != "" {
			media += kv[0] + "=" + kv[1] + " "
		}
	}
	return fmt.Sprintf("IP 质量\nASN: %s (%s)\n类型: %s\n风险分: %s\n流媒体: %s",
		d.Info.Organization, d.Info.ASN, d.Info.Type, d.Score.Scamalytics, strings.TrimRight(media, " "))
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
	case "vpn_deploy":
		// 远程部署 xray VPN 跳板（本节点 = 一个独立出口）。
		// 免密 sudo 自动执行；否则 NEEDS_PRIVILEGE 返回脚本供人工粘贴执行。
		return e.cmdPriv(cmd, timeout, vpnDeployScript())
	default:
		return e.fail(cmd, "未知 net_set action: "+action)
	}
}

// vpnDeployScript 返回在节点上部署 xray 跳板的完整 shell（幂等，多实例隔离）。
func vpnDeployScript() string {
	return `#!/bin/bash
set -uo pipefail
INST=$(hostname 2>/dev/null | tr -cd 'a-zA-Z0-9._-')
INST=${INST:-node1}
PORT=${PORT:-8444}
PATH_WS="/ecp-vpn"
UUID=$(cat /proc/sys/kernel/random/uuid)
DIR=/opt/ecp-xray
XRAY=$DIR/xray
CONF=/etc/ecp-xray/config-${INST}.json
UNIT=ecp-xray-${INST}.service

# 1) xray 二进制（arm64/amd64；Xray-core 新版资产名带后缀）
if [[ ! -x $XRAY ]]; then
  M=$(uname -m)
  case $M in
    aarch64|arm64) FX="arm64-v8a" ;;
    x86_64|amd64) FX="64" ;;
    *) FX=""; echo "UNSUPPORTED_ARCH: $M"; exit 4 ;;
  esac
  VER=$(curl -fsSL --max-time 20 https://api.github.com/repos/XTLS/Xray-core/releases/latest 2>/dev/null | grep -oE '"tag_name": *"[^"]+"' | head -1 | cut -d'"' -f4)
  VER=${VER:-v26.3.27}
  URL="https://github.com/XTLS/Xray-core/releases/download/${VER}/Xray-linux-${FX}.zip"
  TMP=$(mktemp -d)
  if ! curl -fsSL --max-time 150 "$URL" -o "$TMP/x.zip"; then
    echo "DOWNLOAD_FAIL: version=${VER} file=Xray-linux-${FX}.zip 节点无法访问 GitHub 下载，请手动下载 $URL 解压 xray 到 $XRAY 后重跑"; exit 2
  fi
  mkdir -p "$DIR"
  (cd "$TMP" && unzip -oq x.zip && (cp xray "$XRAY" 2>/dev/null || cp Xray "$XRAY" 2>/dev/null))
  chmod +x "$XRAY"; rm -rf "$TMP"
fi

# 2) 配置（vmess+ws 回环，仅 Tunnel 回源可达）
mkdir -p /etc/ecp-xray
cat > "$CONF" <<JSON
{
  "inbounds": [{
    "listen": "127.0.0.1", "port": $PORT, "protocol": "vmess",
    "settings": { "clients": [{ "id": "$UUID", "alterId": 0 }] },
    "streamSettings": { "network": "ws", "wsSettings": { "path": "$PATH_WS" } },
    "sniffing": { "enabled": true, "destOverride": ["http", "tls"] }
  }],
  "outbounds": [
    { "protocol": "freedom", "tag": "direct" },
    { "protocol": "blackhole", "tag": "block" }
  ],
  "routing": {
    "rules": [
      { "type": "field", "protocol": ["bittorrent"], "outboundTag": "block" },
      { "type": "field", "ip": ["geoip:private"], "outboundTag": "direct" }
    ]
  }
}
JSON
chmod 0644 "$CONF"

# 3) systemd 实例化单元 + 启动
cat > /etc/systemd/system/$UNIT <<UNI
[Unit]
Description=ECP VPN Jump ${INST} (xray vmess+ws)
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
ExecStart=$XRAY run -c $CONF
Restart=always
RestartSec=3
[Install]
WantedBy=multi-user.target
UNI
systemctl daemon-reload
systemctl enable --now "$UNIT" >/dev/null 2>&1 || true
sleep 1
if systemctl is-active --quiet "$UNIT"; then
  echo "DEPLOY_OK uuid=$UUID unit=$UNIT listen=127.0.0.1:$PORT$PATH_WS"
  echo "NEXT: Cloudflare Tunnel Public Hostname ${INST}.vpn.<你的域名> -> http://127.0.0.1:$PORT"
else
  echo "DEPLOY_FAILED unit=$UNIT"; journalctl -u "$UNIT" -n 10 --no-pager 2>/dev/null; exit 3
fi`
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

// parseIwScan 把 `iw dev <iface> scan` 的块状输出转成 nmcli 行格式
// （SSID:SIGNAL:SECURITY:CHAN），前端解析逻辑与 nmcli 输出一致，无需改动。
func parseIwScan(out []byte) string {
	var rows []string
	var curSSID, curSec string
	var curSig, curFreq int
	flush := func() {
		if curSSID == "" {
			return
		}
		sec := curSec
		if sec == "" {
			sec = "--"
		}
		chanN := 0
		if curFreq >= 4915 && curFreq <= 5825 { // 5GHz：信道 = (freq-5000)/5
			chanN = (curFreq - 5000) / 5
			if curFreq >= 4915 && curFreq < 5000 {
				chanN = curFreq - 4915 // UNII-1 下沿
			}
		} else if curFreq >= 2412 && curFreq <= 2484 { // 2.4GHz
			chanN = (curFreq - 2407) / 5
		}
		// signal dBm → 0-100 近似（-30→100, -90→10）
		sig := 100 - (-curSig)
		if sig < 0 {
			sig = 0
		}
		if sig > 100 {
			sig = 100
		}
		rows = append(rows, fmt.Sprintf("%s:%d:%s:%d", curSSID, sig, sec, chanN))
		curSSID, curSec, curSig, curFreq = "", "", 0, 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "BSS "):
			flush() // 新块开始，先结算上一块
		case strings.HasPrefix(l, "SSID:"):
			curSSID = strings.TrimSpace(strings.TrimPrefix(l, "SSID:"))
		case strings.HasPrefix(l, "signal:"):
			// signal: -45.00 dBm
			fields := strings.Fields(strings.TrimPrefix(l, "signal:"))
			if len(fields) > 0 {
				v := 0.0
				if _, err := fmt.Sscanf(fields[0], "%f", &v); err == nil {
					curSig = int(v)
				}
			}
		case strings.HasPrefix(l, "freq:"):
			fields := strings.Fields(strings.TrimPrefix(l, "freq:"))
			if len(fields) > 0 {
				fmt.Sscanf(fields[0], "%d", &curFreq)
			}
		case strings.HasPrefix(l, "RSN:"):
			curSec = "WPA2"
		case strings.HasPrefix(l, "WPA:"):
			if curSec == "" {
				curSec = "WPA"
			}
		case strings.HasPrefix(l, "WEP:"):
			if curSec == "" {
				curSec = "WEP"
			}
		}
	}
	flush()
	return strings.Join(rows, "\n")
}

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

// cmdPriv 提权执行一段 shell 脚本：免密 sudo 直行，否则 NEEDS_PRIVILEGE
// 返回脚本供人工在节点上粘贴执行（与 netRunPriv 相同契约，适合多行部署脚本）。
func (e *Executor) cmdPriv(cmd *ecpv1.Command, timeout time.Duration, script string) *ecpv1.CommandResult {
	if sudoOK() {
		tmp, err := os.CreateTemp("", "ecp-priv-*.sh")
		if err != nil {
			return e.fail(cmd, "创建临时脚本失败: "+errString(err))
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		if _, err := tmp.WriteString(script); err != nil {
			tmp.Close()
			return e.fail(cmd, "写临时脚本失败: "+errString(err))
		}
		tmp.Close()
		// 脚本文件在 /tmp 属于当前用户，root 可读；目录可能 sticky（/tmp 人人可写）
		if err := os.Chmod(tmpPath, 0o600); err != nil {
			return e.fail(cmd, "设置脚本权限失败: "+errString(err))
		}
		// 用 exec.CommandContext 完整采集 stdout+stderr（runBin 失败时只回 stderr，
		// 会吞掉脚本写 stdout 的诊断信息）
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		proc := exec.CommandContext(ctx, "sudo", "-n", "bash", tmpPath)
		var so, se bytes.Buffer
		proc.Stdout = &so
		proc.Stderr = &se
		runErr := proc.Run()
		if runErr != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return e.fail(cmd, "部署超时: "+runErr.Error()+"\n"+se.String()+so.String())
			}
			return e.fail(cmd, "部署失败: "+runErr.Error()+"\n"+strings.TrimSpace(se.String())+so.String())
		}
		return e.textResult(cmd, so.String())
	}
	r := e.base(cmd)
	r.Status = ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE
	r.PrivilegeScript = script
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

// stripANSI 清除终端控制序列（进度条 \r、颜色 \x1b[...m、退格等）。
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\r|[\x08\x1b]`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
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