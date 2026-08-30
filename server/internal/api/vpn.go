// VPN / Clash：导出 Clash 客户端配置（用于访问内网设备）。
//
// 模型：每个边缘盒子各自是一个 VPN 跳板节点。节点上跑 xray-core
// （VMess+WS，配各自 Cloudflare Tunnel 暴露为 https://<node>.vpn.<domain>:443），
// Clash 客户端加载本配置后可任选其中一个盒子作出口，内网段走代理隧道。
package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ClashNode 单个 VPN 跳板（一个边缘盒子即一个节点）。
type ClashNode struct {
	Name   string `json:"name"`   // 节点名（默认 hostname）
	Server string `json:"server"` // 该盒子公网入口域名（node1.vpn.yourdomain.com）
	Port   int    `json:"port"`   // 443
	UUID   string `json:"uuid"`   // xray 生成的 UUID
	Path   string `json:"path"`   // ws path（/ecp-vpn）
}

// ClashConfigReq 生成 Clash 配置的请求参数。
type ClashConfigReq struct {
	// Nodes 多个跳板（每个边缘盒子一个）。向后兼容单网关：只填
	// server/uuid 时视为单节点。
	Nodes []ClashNode `json:"nodes"`
	// 兼容旧字段（单节点快速入口）
	Server string `json:"server"`
	Port   int    `json:"port"`
	UUID   string `json:"uuid"`
	Path   string `json:"path"`
	Name   string `json:"name"`
	// ExtraIPs 额外的内网网段（逗号分隔），默认 192.168.0.0/16, 172.16.0.0/12,
	// 10.0.0.0/8, 100.64.0.0/10（tailnet）
	ExtraIPs string `json:"extra_ips"`
}

// ClashConfig 生成 Clash yaml 文本。注意：响应为 text/plain，由前端保存为 .yaml。
func (h *Handler) ClashConfig(c *gin.Context) {
	var in ClashConfigReq
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, codeParam, "参数错误: "+err.Error())
		return
	}
	nodes := in.Nodes
	// 旧单节点字段兼容
	if len(nodes) == 0 && (in.Server != "" || in.UUID != "") {
		n := ClashNode{Name: in.Name, Server: in.Server, Port: in.Port, UUID: in.UUID, Path: in.Path}
		if n.Port <= 0 {
			n.Port = 443
		}
		if n.Path == "" {
			n.Path = "/ecp-vpn"
		}
		if n.Name == "" {
			n.Name = "ecp-vpn"
		}
		nodes = append(nodes, n)
	}
	valid := nodes[:0]
	for _, n := range nodes {
		if n.Server == "" || n.UUID == "" {
			continue
		}
		if n.Port <= 0 {
			n.Port = 443
		}
		if n.Path == "" {
			n.Path = "/ecp-vpn"
		}
		if n.Name == "" {
			n.Name = n.Server
		}
		valid = append(valid, n)
	}
	if len(valid) == 0 {
		fail(c, http.StatusBadRequest, codeParam, "至少需要一个跳板节点（server 与 uuid 必填）")
		return
	}

	yaml := buildClashYAML(valid, in.ExtraIPs)
	c.Data(http.StatusOK, "application/x-yaml; charset=utf-8", []byte(yaml))
}

// buildClashYAML 组装 Clash 配置文本：多个 proxy（每个盒子一个）+ select 组。
func buildClashYAML(nodes []ClashNode, extraIPs string) string {
	ipRules := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"}
	if extraIPs != "" {
		for _, s := range strings.Split(extraIPs, ",") {
			if s = strings.TrimSpace(s); s != "" {
				ipRules = append(ipRules, s)
			}
		}
	}
	var b strings.Builder
	b.WriteString("# ECP VPN 自动生成（多跳板：每个边缘盒子一个出口节点）\n")
	b.WriteString("mixed-port: 7890\n")
	b.WriteString("allow-lan: false\n")
	b.WriteString("mode: rule\n")
	b.WriteString("log-level: info\n\n")
	b.WriteString("proxies:\n")
	for _, in := range nodes {
		b.WriteString("  - name: \"" + in.Name + "\"\n")
		b.WriteString("    type: vmess\n")
		b.WriteString("    server: " + in.Server + "\n")
		b.WriteString(fmt.Sprintf("    port: %d\n", in.Port))
		b.WriteString("    uuid: " + in.UUID + "\n")
		b.WriteString("    alterId: 0\n")
		b.WriteString("    cipher: auto\n")
		b.WriteString("    udp: true\n")
		b.WriteString("    network: ws\n")
		b.WriteString("    ws-opts:\n")
		b.WriteString("      path: \"" + in.Path + "\"\n")
		b.WriteString("      headers:\n")
		b.WriteString("        Host: " + in.Server + "\n\n")
	}
	b.WriteString("proxy-groups:\n")
	b.WriteString("  - name: \"VPN\"\n")
	b.WriteString("    type: select\n")
	b.WriteString("    proxies:\n")
	b.WriteString("      - DIRECT\n")
	for _, in := range nodes {
		b.WriteString("      - \"" + in.Name + "\"\n")
	}
	b.WriteString("  - name: \"自动选择\"\n")
	b.WriteString("    type: url-test\n")
	b.WriteString("    url: http://www.gstatic.com/generate_204\n")
	b.WriteString("    interval: 300\n")
	b.WriteString("    proxies:\n")
	for _, in := range nodes {
		b.WriteString("      - \"" + in.Name + "\"\n")
	}
	b.WriteString("\nrules:\n")
	for _, ip := range ipRules {
		b.WriteString("  - IP-CIDR," + ip + ",VPN,no-resolve\n")
	}
	b.WriteString("  - MATCH,DIRECT\n")
	return b.String()
}